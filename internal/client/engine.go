package client

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/text/encoding/simplifiedchinese"
)

// PREFIXES 脚本行前缀。
var scriptPrefixes = []string{"ALL", "PE", "SYS"}

var varPattern = regexp.MustCompile(`%([A-Za-z_][A-Za-z0-9_]*)%`)
var argPattern = regexp.MustCompile(`"[^"]*"|\S+`)

// IsPE 是否运行在 Windows PE 里。
func IsPE() bool {
	if os.Getenv("MININT") != "" || strings.EqualFold(os.Getenv("SystemDrive"), "X:") {
		return true
	}
	st, err := os.Stat(`X:\Windows\System32`)
	return err == nil && st.IsDir()
}

// stripComment 去掉单引号注释（忽略引号内的）。
func stripComment(line string) string {
	var b strings.Builder
	inQuote := false
	for _, ch := range line {
		if inQuote {
			if ch == '"' {
				inQuote = false
			}
			b.WriteRune(ch)
			continue
		}
		switch ch {
		case '"':
			inQuote = true
			b.WriteRune(ch)
		case '\'':
			return b.String()
		default:
			b.WriteRune(ch)
		}
	}
	return b.String()
}

// splitArgs 按空格切分，保留引号内的空格。
func splitArgs(text string) []string {
	matches := argPattern.FindAllString(text, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, strings.Trim(m, `"`))
	}
	return out
}

// Engine 被控端脚本引擎：解析 ALL/PE/SYS 脚本，执行文件/磁盘/镜像/注册表等指令。
type Engine struct {
	client *Client
	// lastScriptPath 最近一次 diskpart 脚本文件路径（失败时打日志用）
	lastScriptPath string
	dryRun         bool
	vars           map[string]string
	savePath       string
	pe             bool
	handlers       map[string]func(string)
}

// NewEngine 创建引擎。
func NewEngine(c *Client, dryRun bool) *Engine {
	e := &Engine{
		client: c, dryRun: dryRun, vars: map[string]string{},
		savePath: `D:\Receive\`, pe: IsPE(),
	}
	e.initBuiltinVars()
	e.registerHandlers()
	return e
}

func (e *Engine) log(text string) { e.client.Log(text) }

func (e *Engine) initBuiltinVars() {
	host, _ := os.Hostname()
	sysRoot := os.Getenv("SystemRoot")
	if sysRoot == "" {
		sysRoot = `C:\Windows`
	}
	tmp := os.Getenv("TEMP")
	if tmp == "" {
		tmp = filepath.Join(sysRoot, "Temp")
	}
	e.vars = map[string]string{
		"MAXFREE":         e.maxFreeDrive() + ":",
		"SAVEFILEPATH":    e.savePath,
		"CLIENTNUM":       "001",
		"APPPATH":         e.client.ExeDir(),
		"SERVERIP":        e.client.Server,
		"CLIENTIP":        "-",
		"WINPATH":         sysRoot,
		"SYSPATH":         filepath.Join(sysRoot, "System32"),
		"TMPPATH":         tmp,
		"DESKPATH":        filepath.Join(os.Getenv("USERPROFILE"), "Desktop"),
		"ALLUSERDESKPATH": `C:\Users\Public\Desktop`,
		"COMPUTERNAME":    host,
	}
}

// maxFreeDrive 找剩余空间最大的分区。
func (e *Engine) maxFreeDrive() string {
	best, bestFree := "C", uint64(0)
	for _, letter := range "CDEFGHIJKLMNOPQRSTUVWXYZ" {
		root := string(letter) + ":\\"
		ptr, err := windows.UTF16PtrFromString(root)
		if err != nil {
			continue
		}
		var free, total, avail uint64
		if err := windows.GetDiskFreeSpaceEx(ptr, &free, &total, &avail); err != nil {
			continue
		}
		if free > bestFree {
			best, bestFree = string(letter), free
		}
	}
	return best
}

// Expand 替换 %变量%。
func (e *Engine) Expand(text string) string {
	return varPattern.ReplaceAllStringFunc(text, func(m string) string {
		key := strings.ToUpper(m[1 : len(m)-1])
		if v, ok := e.vars[key]; ok {
			return v
		}
		return m
	})
}

// Run 执行外部命令，返回 (返回码, 输出)。dryRun 时只记录。
func (e *Engine) Run(cmdline string, wait bool, timeout int) (int, string) {
	cmdline = e.Expand(cmdline)
	if e.dryRun {
		e.log("[模拟] " + cmdline)
		return 0, ""
	}
	cmd := exec.Command("cmd", "/c", cmdline)
	hideWindow(cmd)
	if !wait {
		if err := cmd.Start(); err != nil {
			e.log(fmt.Sprintf("命令执行失败：%s (%v)", cmdline, err))
			return -1, ""
		}
		return 0, ""
	}

	done := make(chan struct{})
	var out []byte
	var runErr error
	go func() {
		out, runErr = cmd.CombinedOutput()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Duration(timeout) * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		e.log("命令超时：" + cmdline)
		return -1, "timeout"
	}
	text := decodeOEM(out)
	code := 0
	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			code = -1
		}
	}
	return code, strings.TrimSpace(text)
}

// decodeOEM 把控制台输出（GBK）转成 UTF-8。
func decodeOEM(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if utf8.Valid(b) {
		return string(b)
	}
	if dec, err := simplifiedchinese.GBK.NewDecoder().Bytes(b); err == nil {
		return string(dec)
	}
	return string(b)
}

// encodeGBK diskpart 按 OEM 代码页读脚本，中文卷标必须用同一个编码写。
func encodeGBK(s string) []byte {
	if out, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(s)); err == nil {
		return out
	}
	return []byte(s)
}

// ---------------------------------------------------------------- 脚本解释

// RunScript 逐行解释执行脚本。
func (e *Engine) RunScript(text string) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}

	labels := map[string]int{}
	for i, raw := range lines {
		s := strings.TrimSpace(stripComment(raw))
		if strings.HasPrefix(s, ":") {
			labels[strings.ToUpper(strings.TrimSpace(s[1:]))] = i
		}
	}
	pairs := e.matchPairs(lines)

	pc := 0
	guard := 0
	for pc < len(lines) {
		guard++
		if guard > 200000 {
			e.log("脚本执行次数超限，已中止")
			return
		}
		raw := lines[pc]
		s := strings.TrimSpace(stripComment(raw))
		if s == "" || strings.HasPrefix(s, ":") {
			pc++
			continue
		}
		parts := strings.SplitN(s, " ", 2)
		prefix := strings.ToUpper(parts[0])
		if !isPrefix(prefix) {
			pc++ // 无前缀 → 跳过
			continue
		}
		if (prefix == "PE" && !e.pe) || (prefix == "SYS" && e.pe) {
			pc++
			continue
		}
		body := ""
		if len(parts) > 1 {
			body = strings.TrimSpace(parts[1])
		}
		pc = e.execLine(pc, body, labels, pairs, lines)
	}
}

func isPrefix(s string) bool {
	for _, p := range scriptPrefixes {
		if p == s {
			return true
		}
	}
	return false
}

// matchPairs 配对 FOR/NEXT、WHILE/WEND、IF/END IF。
func (e *Engine) matchPairs(lines []string) map[int]int {
	type item struct {
		word string
		idx  int
	}
	var stack []item
	pairs := map[int]int{}
	for i, raw := range lines {
		s := strings.TrimSpace(stripComment(raw))
		if s == "" {
			continue
		}
		tok := strings.SplitN(s, " ", 2)
		word := ""
		if len(tok) > 1 {
			word = strings.ToUpper(strings.Fields(tok[1])[0])
		}
		switch word {
		case "FOR", "WHILE", "IF":
			stack = append(stack, item{word, i})
		case "NEXT":
			for j := len(stack) - 1; j >= 0; j-- {
				if stack[j].word == "FOR" {
					pairs[stack[j].idx] = i
					pairs[i] = stack[j].idx
					stack = append(stack[:j], stack[j+1:]...)
					break
				}
			}
		case "WEND":
			for j := len(stack) - 1; j >= 0; j-- {
				if stack[j].word == "WHILE" {
					pairs[stack[j].idx] = i
					pairs[i] = stack[j].idx
					stack = append(stack[:j], stack[j+1:]...)
					break
				}
			}
		case "ENDIF":
			for j := len(stack) - 1; j >= 0; j-- {
				if stack[j].word == "IF" {
					pairs[stack[j].idx] = i
					pairs[i] = stack[j].idx
					stack = append(stack[:j], stack[j+1:]...)
					break
				}
			}
		default:
			if strings.HasSuffix(strings.ToUpper(s), "END IF") {
				for j := len(stack) - 1; j >= 0; j-- {
					if stack[j].word == "IF" {
						pairs[stack[j].idx] = i
						pairs[i] = stack[j].idx
						stack = append(stack[:j], stack[j+1:]...)
						break
					}
				}
			}
		}
	}
	return pairs
}

func (e *Engine) execLine(pc int, body string, labels map[string]int,
	pairs map[int]int, lines []string) int {
	if body == "" {
		return pc + 1
	}
	word, rest := splitWord(body)
	switch word {
	case "GOTO":
		target := strings.ToUpper(strings.TrimSpace(rest))
		if idx, ok := labels[target]; ok {
			return idx
		}
		e.log("GOTO 目标不存在：" + target)
		return pc + 1
	case "IF":
		return e.execIf(pc, rest, pairs, lines)
	case "FOR":
		return e.execFor(pc, rest, pairs)
	case "NEXT":
		return e.execNext(pc, pairs, lines)
	case "WHILE":
		if !e.evalCond(rest) {
			return pairOr(pairs, pc, len(lines)) + 1
		}
		return pc + 1
	case "WEND":
		return pairOr(pairs, pc, pc)
	}
	if h, ok := e.handlers[word]; ok {
		func() {
			defer func() {
				if r := recover(); r != nil {
					e.log(fmt.Sprintf("指令 %s 执行异常：%v", word, r))
				}
			}()
			h(rest)
		}()
		return pc + 1
	}
	e.log("跳过未知指令 " + word)
	return pc + 1
}

// ExecRaw 执行单条指令文本（不带 ALL/PE/SYS 前缀）。
func (e *Engine) ExecRaw(line string) {
	line = strings.TrimSpace(stripComment(line))
	if line == "" {
		return
	}
	word, rest := splitWord(line)
	if isPrefix(word) {
		word, rest = splitWord(rest)
	}
	h, ok := e.handlers[word]
	if !ok {
		e.log(fmt.Sprintf("未知指令 %s（原文：%s）", word, line))
		return
	}
	defer func() {
		if r := recover(); r != nil {
			e.log(fmt.Sprintf("指令 %s 异常：%v", word, r))
		}
	}()
	h(rest)
}

func splitWord(s string) (string, string) {
	s = strings.TrimSpace(s)
	idx := strings.IndexAny(s, " \t")
	if idx < 0 {
		return strings.ToUpper(s), ""
	}
	return strings.ToUpper(s[:idx]), strings.TrimSpace(s[idx+1:])
}

func pairOr(pairs map[int]int, key, def int) int {
	if v, ok := pairs[key]; ok {
		return v
	}
	return def
}

var ifThenRe = regexp.MustCompile(`(?is)^(.*?)\s+THEN\s*(.*)$`)
var elseRe = regexp.MustCompile(`(?is)\s+ELSE\s+`)

func (e *Engine) execIf(pc int, rest string, pairs map[int]int, lines []string) int {
	m := ifThenRe.FindStringSubmatch(rest)
	if m == nil {
		return pc + 1
	}
	cond, thenPart := m[1], strings.TrimSpace(m[2])
	ok := e.evalCond(cond)
	if thenPart != "" {
		// 单行 IF ... THEN x ELSE y
		parts := elseRe.Split(thenPart, 2)
		branch := ""
		if ok {
			branch = parts[0]
		} else if len(parts) > 1 {
			branch = parts[1]
		}
		if branch != "" {
			e.execInline(branch)
		}
		return pc + 1
	}
	// 块状 IF ... THEN / ... / END IF
	end := pairOr(pairs, pc, len(lines))
	if ok {
		return pc + 1
	}
	return end + 1
}

// execInline 执行 THEN/ELSE 后面的单条指令（可能带前缀）。
func (e *Engine) execInline(text string) {
	text = strings.TrimSpace(text)
	word, rest := splitWord(text)
	if isPrefix(word) {
		word, rest = splitWord(rest)
	}
	if h, ok := e.handlers[word]; ok {
		defer func() {
			if r := recover(); r != nil {
				e.log(fmt.Sprintf("指令 %s 异常：%v", word, r))
			}
		}()
		h(rest)
	}
}

var forRe = regexp.MustCompile(`(?is)^(\w+)\s*=\s*(.+?)\s+TO\s+(.+?)(?:\s+STEP\s+(.+))?$`)
var forVarRe = regexp.MustCompile(`(?i)^(\w+)\s*=`)

func (e *Engine) execFor(pc int, rest string, pairs map[int]int) int {
	m := forRe.FindStringSubmatch(rest)
	if m == nil {
		return pc + 1
	}
	name := m[1]
	startS, endS, stepS := m[2], m[3], m[4]
	if stepS == "" {
		stepS = "1"
	}
	cur, err1 := strconv.ParseFloat(strings.TrimSpace(e.Expand(startS)), 64)
	limit, err2 := strconv.ParseFloat(strings.TrimSpace(e.Expand(endS)), 64)
	inc, err3 := strconv.ParseFloat(strings.TrimSpace(e.Expand(stepS)), 64)
	if err1 != nil || err2 != nil || err3 != nil {
		e.log("FOR 参数无效：" + rest)
		return pairOr(pairs, pc, pc) + 1
	}
	key := strings.ToUpper(name)
	e.vars[key] = strconv.FormatInt(int64(cur), 10)
	e.vars[fmt.Sprintf("__FOR_%d_END", pc)] = strconv.FormatFloat(limit, 'f', -1, 64)
	e.vars[fmt.Sprintf("__FOR_%d_STEP", pc)] = strconv.FormatFloat(inc, 'f', -1, 64)
	if (inc >= 0 && cur > limit) || (inc < 0 && cur < limit) {
		return pairOr(pairs, pc, pc) + 1
	}
	return pc + 1
}

func (e *Engine) execNext(pc int, pairs map[int]int, lines []string) int {
	start := pairOr(pairs, pc, pc)
	name := ""
	if start < len(lines) {
		_, body := splitWord(lines[start])
		if m := forVarRe.FindStringSubmatch(strings.TrimPrefix(body, "FOR ")); m != nil {
			name = strings.ToUpper(m[1])
		}
	}
	if name == "" {
		return pc + 1
	}
	cur, err1 := strconv.ParseFloat(e.vars[name], 64)
	step, err2 := strconv.ParseFloat(e.vars[fmt.Sprintf("__FOR_%d_STEP", start)], 64)
	limit, err3 := strconv.ParseFloat(e.vars[fmt.Sprintf("__FOR_%d_END", start)], 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return pc + 1
	}
	cur += step
	e.vars[name] = strconv.FormatInt(int64(cur), 10)
	if (step >= 0 && cur <= limit) || (step < 0 && cur >= limit) {
		return start + 1
	}
	return pc + 1
}

var (
	existRe = regexp.MustCompile(`(?is)^EXIST\s+(.+)$`)
	notRe   = regexp.MustCompile(`(?is)^NOT\s+(.+)$`)
)

func (e *Engine) evalCond(expr string) bool {
	expr = strings.TrimSpace(e.Expand(expr))
	if m := existRe.FindStringSubmatch(expr); m != nil {
		return fileExistsAny(strings.Trim(strings.TrimSpace(m[1]), `"`))
	}
	if m := notRe.FindStringSubmatch(expr); m != nil {
		return !e.evalCond(m[1])
	}
	upper := strings.ToUpper(expr)
	if idx := strings.Index(upper, " OR "); idx >= 0 {
		return e.evalCond(expr[:idx]) || e.evalCond(expr[idx+4:])
	}
	if idx := strings.Index(upper, " AND "); idx >= 0 {
		return e.evalCond(expr[:idx]) && e.evalCond(expr[idx+5:])
	}
	for _, op := range []string{"<>", ">=", "<=", "=", ">", "<"} {
		if idx := strings.Index(expr, op); idx >= 0 {
			left, right := expr[:idx], expr[idx+len(op):]
			return compareVals(numOrStr(left), numOrStr(right), op)
		}
	}
	return expr != ""
}

func compareVals(a, b any, op string) bool {
	fa, aok := a.(float64)
	fb, bok := b.(float64)
	if aok && bok {
		switch op {
		case "<>":
			return fa != fb
		case ">=":
			return fa >= fb
		case "<=":
			return fa <= fb
		case "=":
			return fa == fb
		case ">":
			return fa > fb
		case "<":
			return fa < fb
		}
	}
	sa, sb := fmt.Sprint(a), fmt.Sprint(b)
	switch op {
	case "<>":
		return sa != sb
	case ">=":
		return sa >= sb
	case "<=":
		return sa <= sb
	case "=":
		return strings.EqualFold(sa, sb)
	case ">":
		return sa > sb
	case "<":
		return sa < sb
	}
	return false
}

func numOrStr(text string) any {
	t := strings.Trim(strings.TrimSpace(text), `"`)
	if f, err := strconv.ParseFloat(t, 64); err == nil {
		return f
	}
	return t
}

func fileExistsAny(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// ---------------------------------------------------------------- 指令表

func (e *Engine) registerHandlers() {
	e.handlers = map[string]func(string){
		"ADDLOG":            e.cmdADDLOG,
		"SETSTRMSGTOSERVER": e.cmdSETSTRMSGTOSERVER,
		"SETVAR":            e.cmdSETVAR,
		"DIM":               e.cmdDIM,
		"SLEEP":             e.cmdSLEEP,
		"WAITUSERLOGON":     e.cmdWAITUSERLOGON,

		"COPY":         e.cmdCOPY,
		"COPYD":        e.cmdCOPYD,
		"DEL":          e.cmdDEL,
		"DELD":         e.cmdDELD,
		"DELFOLDER":    e.cmdDELD,
		"CREATEFOLDER": e.cmdCREATEFOLDER,
		"WRITETXT":     e.cmdWRITETXT,
		"WRITETXTADD":  e.cmdWRITETXTADD,
		"GETFILESIZE":  e.cmdGETFILESIZE,
		"SETSAVEPATH":  e.cmdSETSAVEPATH,
		"KILL":         e.cmdKILL,
		"DOWNLOAD":     e.cmdDOWNLOAD,

		"AUTOPARTITION": e.cmdAUTOPARTITION,
		"UNCOMPRESS":    e.cmdUNCOMPRESS,
		"COMPRESS":      e.cmdCOMPRESS,
		"FIXBOOT":       e.cmdFIXBOOT,
		"REBUILD_MBR":   e.cmdREBUILDMBR,
		"INJECTDRV":     e.cmdINJECTDRV,
		"STREAMINSTALL": e.cmdSTREAMINSTALL,

		"SETCOMPUTERNAME": e.cmdSETCOMPUTERNAME,
		"SETBOOT":         e.cmdSETBOOT,
		"REBOOT":          e.cmdREBOOT,
		"POWEROFF":        e.cmdPOWEROFF,
		"INJECTCLIENT":    e.cmdINJECTCLIENT,
		"INJECTAUTO":      e.cmdINJECTAUTO,
		"INJECTVNC":       e.cmdINJECTVNC,
		"INJECTPEDRV":     e.cmdINJECTPEDRV,
		"UPDATECLIENT":    e.cmdUPDATECLIENT,

		"SETREGSZ":       e.cmdSETREGSZ,
		"SETREGDWORD":    e.cmdSETREGDWORD,
		"GETREGSZ":       e.cmdGETREGSZ,
		"GETREGDWORD":    e.cmdGETREGSZ,
		"REGKEYEXISTS":   e.cmdREGKEYEXISTS,
		"REGVALUEEXISTS": e.cmdREGVALUEEXISTS,
		"CREATEKEY":      e.cmdCREATEKEY,
		"DELREGKEY":      e.cmdDELREGKEY,
		"DELREGVAL":      e.cmdDELREGVAL,

		"SHELL":     e.cmdSHELL,
		"SHELLWAIT": e.cmdSHELLWAIT,
	}
}

// ---------------------------------------------------------------- 基础指令

func (e *Engine) cmdADDLOG(rest string) {
	e.log(strings.Trim(strings.TrimSpace(e.Expand(rest)), `"`))
}

func (e *Engine) cmdSETSTRMSGTOSERVER(rest string) {
	msg := strings.Trim(strings.TrimSpace(e.Expand(rest)), `"`)
	e.log("[上报] " + msg)
	e.client.SendLog(msg)
}

func (e *Engine) cmdSETVAR(rest string) {
	if idx := strings.Index(rest, "="); idx >= 0 {
		name := strings.ToUpper(strings.TrimSpace(rest[:idx]))
		e.vars[name] = strings.TrimSpace(e.Expand(rest[idx+1:]))
		return
	}
	parts := strings.SplitN(strings.TrimSpace(rest), " ", 2)
	if len(parts) > 0 && parts[0] != "" {
		v := ""
		if len(parts) > 1 {
			v = e.Expand(parts[1])
		}
		e.vars[strings.ToUpper(parts[0])] = v
	}
}

var dimRe = regexp.MustCompile(`(?is)^(\w+)\s+AS\s+\w+\s*(?:=\s*(.*))?$`)

func (e *Engine) cmdDIM(rest string) {
	if m := dimRe.FindStringSubmatch(strings.TrimSpace(rest)); m != nil {
		v := "0"
		if m[2] != "" {
			v = e.Expand(m[2])
		}
		e.vars[strings.ToUpper(m[1])] = v
	}
}

func (e *Engine) cmdSLEEP(rest string) {
	ms := 0
	if f, err := strconv.ParseFloat(strings.TrimSpace(e.Expand(rest)), 64); err == nil {
		ms = int(f)
	}
	if e.dryRun || ms <= 0 {
		return
	}
	time.Sleep(time.Duration(ms) * time.Millisecond)
}

func (e *Engine) cmdWAITUSERLOGON(rest string) {
	e.log("等待用户登录…")
	for i := 0; i < 600; i++ {
		if e.userLoggedOn() {
			return
		}
		if e.dryRun {
			return
		}
		time.Sleep(time.Second)
	}
	e.log("等待用户登录超时")
}

func (e *Engine) userLoggedOn() bool {
	_, out := e.Run("query user", true, 10)
	return strings.Contains(out, "Active") || strings.Contains(out, "运行")
}

// ---------------------------------------------------------------- 文件指令

func (e *Engine) cmdCOPY(rest string) {
	a := splitArgs(e.Expand(rest))
	if len(a) < 2 {
		return
	}
	if e.dryRun {
		e.log(fmt.Sprintf("[模拟] copy %s -> %s", a[0], a[1]))
		return
	}
	if err := copyFile(a[0], a[1]); err != nil {
		e.log("复制失败：" + err.Error())
	}
}

func (e *Engine) cmdCOPYD(rest string) {
	a := splitArgs(e.Expand(rest))
	if len(a) < 2 || e.dryRun {
		return
	}
	if err := copyDir(a[0], a[1]); err != nil {
		e.log("复制目录失败：" + err.Error())
	}
}

func (e *Engine) cmdDEL(rest string) {
	p := strings.Trim(strings.TrimSpace(e.Expand(rest)), `"`)
	if e.dryRun {
		return
	}
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		_ = os.Remove(p)
	}
}

func (e *Engine) cmdDELD(rest string) {
	p := strings.Trim(strings.TrimSpace(e.Expand(rest)), `"`)
	if e.dryRun {
		return
	}
	if st, err := os.Stat(p); err == nil && st.IsDir() {
		_ = os.RemoveAll(p)
	}
}

func (e *Engine) cmdCREATEFOLDER(rest string) {
	p := strings.Trim(strings.TrimSpace(e.Expand(rest)), `"`)
	if e.dryRun {
		return
	}
	_ = os.MkdirAll(p, 0o755)
}

func (e *Engine) cmdWRITETXT(rest string) {
	a := splitArgs(e.Expand(rest))
	if len(a) < 2 || e.dryRun {
		return
	}
	_ = os.MkdirAll(filepath.Dir(a[0]), 0o755)
	_ = os.WriteFile(a[0], encodeGBK(a[1]), 0o644)
}

func (e *Engine) cmdWRITETXTADD(rest string) {
	a := splitArgs(e.Expand(rest))
	if len(a) < 2 || e.dryRun {
		return
	}
	fh, err := os.OpenFile(a[0], os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer fh.Close()
	_, _ = fh.Write(encodeGBK(a[1] + "\r\n"))
}

func (e *Engine) cmdGETFILESIZE(rest string) {
	a := splitArgs(e.Expand(rest))
	if len(a) < 2 {
		return
	}
	size := "0"
	if st, err := os.Stat(a[0]); err == nil {
		size = strconv.FormatInt(st.Size(), 10)
	}
	e.vars[strings.ToUpper(a[1])] = size
}

func (e *Engine) cmdSETSAVEPATH(rest string) {
	e.savePath = strings.Trim(strings.TrimSpace(e.Expand(rest)), `"`)
	e.vars["SAVEFILEPATH"] = e.savePath
}

func (e *Engine) cmdKILL(rest string) {
	name := strings.Trim(strings.TrimSpace(e.Expand(rest)), `"`)
	e.Run(fmt.Sprintf(`taskkill /f /im "%s"`, name), true, 60)
}

func (e *Engine) cmdDOWNLOAD(rest string) {
	a := splitArgs(e.Expand(rest))
	if len(a) == 0 {
		return
	}
	url := a[0]
	dir := e.savePath
	if len(a) > 1 {
		dir = a[1]
	}
	if e.dryRun {
		e.log(fmt.Sprintf("[模拟] 下载 %s -> %s", url, dir))
		return
	}
	name := url[strings.LastIndex(url, "/")+1:]
	if name == "" {
		name = "download.bin"
	}
	_ = os.MkdirAll(dir, 0o755)
	dest := filepath.Join(dir, name)
	e.log("开始下载 " + url)
	client := &http.Client{Timeout: 30 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		e.log(fmt.Sprintf("下载失败 %s: %v", url, err))
		return
	}
	defer resp.Body.Close()
	fh, err := os.Create(dest)
	if err != nil {
		e.log("写入失败：" + err.Error())
		return
	}
	defer fh.Close()
	n, err := io.Copy(fh, resp.Body)
	if err != nil {
		e.log("下载中断：" + err.Error())
		return
	}
	e.log(fmt.Sprintf("下载完成 %s (%d B)", dest, n))
}

// ---------------------------------------------------------------- 磁盘 / 镜像

func (e *Engine) cmdAUTOPARTITION(rest string) {
	disk := strings.TrimSpace(e.Expand(rest))
	if disk == "" {
		disk = "0"
	}
	e.log("开始自动分区 磁盘" + disk)
	script := "select disk " + disk + "\r\nclean\r\nconvert gpt\r\n" +
		"create partition efi size=512\r\nformat fs=Fat32 quick\r\nassign letter=S\r\n" +
		"create partition msr size=128\r\n" +
		"create partition primary\r\nformat fs=NTFS quick\r\nassign letter=C\r\nexit\r\n"
	e.diskpart(script)
	e.log("分区任务完成")
}

// selectedDisk 从脚本里取出 "Select disk N" 的 N。
func selectedDisk(script string) (int, bool) {
	for _, line := range strings.Split(script, "\n") {
		t := strings.TrimSpace(strings.TrimRight(line, "\r"))
		if len(t) > 11 && strings.EqualFold(t[:11], "select disk") {
			n := 0
			if _, err := fmt.Sscanf(strings.TrimSpace(t[11:]), "%d", &n); err == nil {
				return n, true
			}
		}
	}
	return 0, false
}

// diskpart 执行 diskpart 脚本（rem POST xxx 会在之后执行）。
func (e *Engine) diskpart(script string) {
	if e.dryRun {
		e.log("[模拟] diskpart:\n" + script)
		return
	}
	// 整盘重分区前先处理盘符冲突：assign letter=D 失败会让 diskpart 中止整个脚本，
	// 表现就是"选了 3 个分区只出来 1 个"。
	if strings.Contains(strings.ToLower(script), "clean") {
		script = e.prepareDriveLetters(script)
	}
	rc, out := e.runDiskpartText(script, 3600)
	if rc != 0 && strings.TrimSpace(out) == "" {
		e.log(fmt.Sprintf("diskpart 没有任何输出（返回码 %d），脚本文件：%s", rc, e.lastScriptPath))
	}
	if out != "" {
		text := out
		if len(text) > 800 {
			text = text[len(text)-800:]
		}
		e.log(text)
	}
	if rc != 0 {
		e.log(fmt.Sprintf("diskpart 返回 %d", rc))
	}
	if strings.Contains(out, "驱动器号") || strings.Contains(strings.ToLower(out), "not available") {
		e.log("提示：assign letter 失败说明盘符被占用（光驱/U盘/旧分区）。" +
			"已自动尝试腾出或改用空闲盘符；若仍失败，请先给光驱换个盘符再重试。")
	}
	// 跑完列一下分区结果，能直接看出建了几个、有没有漏
	if disk, ok := selectedDisk(script); ok {
		if _, list := e.runDiskpartText(
			fmt.Sprintf("select disk %d\r\nlist partition\r\nexit\r\n", disk), 120); list != "" {
			lines := strings.Split(strings.TrimSpace(list), "\n")
			if len(lines) > 8 {
				lines = lines[len(lines)-8:]
			}
			e.log("分区结果：\n" + strings.Join(lines, "\n"))
		}
	}
	// 脚本里的 rem POST <命令> 在 diskpart 之后执行（如重建主引导记录）
	for _, line := range strings.Split(script, "\n") {
		s := strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(s), "rem post ") {
			cmd := strings.TrimSpace(s[9:])
			e.log("执行后续命令：" + cmd)
			e.Run(cmd, true, 600)
		}
	}
}

func (e *Engine) cmdUNCOMPRESS(rest string) {
	a := splitArgs(e.Expand(rest))
	if len(a) == 0 {
		return
	}
	image := a[0]
	index := 1
	if idx := strings.Index(image, "|"); idx >= 0 {
		index, _ = strconv.Atoi(image[idx+1:])
		image = image[:idx]
		if index <= 0 {
			index = 1
		}
	}
	target := "C:"
	if len(a) > 1 {
		target = a[1]
	}
	compact := len(a) > 2 && strings.EqualFold(a[2], "TRUE")
	e.log(fmt.Sprintf("开始还原 %s 卷%d -> %s（压缩=%v）", image, index, target, compact))
	wimboot := ""
	if compact {
		wimboot = "--wimboot"
	}
	rc, _ := e.Run(fmt.Sprintf(`wimlib-imagex.exe apply "%s" %d "%s" %s --check`,
		image, index, target, wimboot), true, 7200)
	if rc != 0 {
		e.log("wimlib 不可用，改用 dism")
		e.Run(fmt.Sprintf(`dism /Apply-Image /ImageFile:"%s" /Index:%d /ApplyDir:"%s"`,
			image, index, target), true, 7200)
	}
	e.log("还原完成 " + target)
}

func (e *Engine) cmdCOMPRESS(rest string) {
	a := splitArgs(e.Expand(rest))
	if len(a) < 2 {
		return
	}
	source, image := a[0], a[1]
	incremental := len(a) > 2 && strings.EqualFold(a[2], "TRUE")
	e.log(fmt.Sprintf("开始%s备份 %s -> %s",
		map[bool]string{true: "增量", false: ""}[incremental], source, image))
	if incremental && fileExistsAny(image) {
		e.Run(fmt.Sprintf(`wimlib-imagex.exe update "%s" "%s" --check`, image, source), true, 7200)
	} else {
		e.Run(fmt.Sprintf(`wimlib-imagex.exe capture "%s" "%s" --check`, source, image), true, 7200)
	}
	e.log("备份任务完成")
}

func (e *Engine) cmdFIXBOOT(rest string) {
	a := splitArgs(e.Expand(rest))
	win := "C:"
	if len(a) > 0 {
		win = a[0]
	}
	boot := ""
	if len(a) > 1 {
		boot = a[1]
	}
	opt := "0"
	if len(a) > 2 {
		opt = a[2]
	}
	firmware := "ALL"
	switch opt {
	case "1":
		firmware = "BIOS"
	case "2":
		firmware = "UEFI"
	}
	winDir := filepath.Join(strings.TrimRight(win, "\\")+"\\", "Windows")
	if boot != "" && !strings.EqualFold(boot, "0") && !strings.EqualFold(boot, "AUTO") {
		e.Run(fmt.Sprintf("format %s /q /y", boot), true, 600)
	} else {
		boot = ""
	}
	shown := boot
	if shown == "" {
		shown = "自动检测"
	}
	e.log(fmt.Sprintf("修复引导 %s 引导分区=%s 固件=%s", winDir, shown, firmware))
	// 不带 /s 时 bcdboot 会自己找 EFI 系统分区，比硬给盘符可靠
	sArg := ""
	if boot != "" {
		sArg = "/s " + boot
	}
	e.Run(fmt.Sprintf(`bcdboot.exe "%s" %s /f %s /l zh-cn`, winDir, sArg, firmware), true, 1800)
}

func (e *Engine) cmdREBUILDMBR(rest string) {
	e.log("重建主引导（仅 MBR 磁盘）")
	e.Run("bootsect.exe /nt60 SYS /force", true, 600)
}

func (e *Engine) cmdINJECTDRV(rest string) {
	a := splitArgs(e.Expand(rest))
	if len(a) < 2 {
		return
	}
	e.log(fmt.Sprintf("注入驱动 %s -> %s", a[1], a[0]))
	e.Run(fmt.Sprintf(`dism /Image:%s /Add-Driver /Driver:"%s" /Recurse /forceunsigned`,
		a[0], a[1]), true, 3600)
}

func (e *Engine) cmdSTREAMINSTALL(rest string) {
	a := splitArgs(e.Expand(rest))
	if len(a) == 0 {
		return
	}
	url := a[0]
	part := 1
	if len(a) > 1 {
		if n, err := strconv.Atoi(a[1]); err == nil {
			part = n
		}
	}
	target := string(rune('A'+part-1)) + ":"
	e.log(fmt.Sprintf("流式安装 %s -> %s", url, target))
	if len(a) > 3 && a[3] == "1" {
		e.Run(fmt.Sprintf("format %s /q /y", target), true, 600)
	}
	e.Run(fmt.Sprintf(`wimlib-imagex.exe apply "%s" 1 "%s" --check`, url, target), true, 7200)
	e.log("流式安装完成")
}

// ---------------------------------------------------------------- 系统 / 注入

var setComputerNameW = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetComputerNameW")

func (e *Engine) cmdSETCOMPUTERNAME(rest string) {
	name := strings.Trim(strings.TrimSpace(e.Expand(rest)), `"`)
	if name == "" {
		return
	}
	e.log("设置计算机名 " + name)
	if e.dryRun {
		return
	}
	if ptr, err := windows.UTF16PtrFromString(name); err == nil {
		_, _, _ = setComputerNameW.Call(uintptr(unsafe.Pointer(ptr)))
	}
	e.regSet("HKLM", `SYSTEM\CurrentControlSet\Control\ComputerName\ComputerName`,
		"ComputerName", name)
	e.regSet("HKLM", `SYSTEM\CurrentControlSet\Services\Tcpip\Parameters`, "Hostname", name)
}

func (e *Engine) cmdSETBOOT(rest string) {
	mode := strings.TrimSpace(e.Expand(rest))
	name := "网络"
	if mode == "1" {
		name = "本地"
	}
	e.log("设置下次启动：" + name)
	if e.dryRun {
		return
	}
	if mode == "1" {
		e.Run("bcdedit /set {fwbootmgr} displayorder {bootmgr} /addfirst", true, 120)
	} else {
		e.Run(`bcdedit /set {bootmgr} path \EFI\Microsoft\Boot\bootmgfw.efi`, true, 120)
	}
}

func (e *Engine) cmdREBOOT(rest string) {
	e.log("执行重启指令")
	if e.pe {
		e.Run("wpeutil reboot", false, 60)
	} else {
		e.Run("shutdown /r /t 3 /f", false, 60)
	}
}

func (e *Engine) cmdPOWEROFF(rest string) {
	e.log("执行关机指令")
	if e.pe {
		e.Run("wpeutil shutdown", false, 60)
	} else {
		e.Run("shutdown /s /t 3 /f", false, 60)
	}
}

func (e *Engine) cmdINJECTCLIENT(rest string) {
	target := strings.Trim(strings.TrimSpace(e.Expand(rest)), `"`)
	if target == "" {
		target = "C:"
	}
	e.log("注入客户端到 " + target)
	src := filepath.Join(e.client.ExeDir(), "DP-PXEClient.exe")
	if !fileExistsAny(src) {
		// Go 版本只有一个可执行文件，用自身代替
		if self, err := os.Executable(); err == nil {
			src = self
		}
	}
	if !fileExistsAny(src) {
		e.log("找不到客户端本体：" + src)
		return
	}
	dstDir := filepath.Join(strings.TrimRight(target, "\\")+"\\", "Windows", "System32")
	if e.dryRun {
		e.log(fmt.Sprintf("[模拟] 复制 %s -> %s", src, dstDir))
		return
	}
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		e.log("复制失败：" + err.Error())
		return
	}
	dst := filepath.Join(dstDir, "DP-PXEClient.exe")
	if err := copyFile(src, dst); err != nil {
		e.log("复制失败：" + err.Error())
		return
	}
	e.Run(fmt.Sprintf(`sc create DP-PXEClient binPath= "%s" start= auto obj= LocalSystem DisplayName= "DP-PXEClient"`, dst), true, 120)
	e.log("注入客户端成功")
}

const unattendXML = `<?xml version="1.0" encoding="utf-8"?>
<unattend xmlns="urn:schemas-microsoft-com:unattend">
  <settings pass="oobeSystem">
    <component name="Microsoft-Windows-Shell-Setup" processorArchitecture="amd64"
      publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <AutoLogon><Enabled>true</Enabled><LogonCount>1</LogonCount>
        <Username>Administrator</Username></AutoLogon>
      <OOBE><SkipMachineOOBE>true</SkipMachineOOBE></OOBE>
    </component>
  </settings>
</unattend>
`

const tightVncReg = `Windows Registry Editor Version 5.00

[HKEY_CURRENT_USER\Software\TightVNC\Server]
"RfbPort"=dword:0000170c
"HttpPort"=dword:000016a8
"Password"=hex:49,40,15,f9,a3,5e,8b,22
"AcceptRfbConnections"=dword:00000001
"EnableFileTransfers"=dword:00000001
"UseVncAuthentication"=dword:00000001
"RunControlInterface"=dword:00000001
`

func (e *Engine) cmdINJECTAUTO(rest string) {
	target := strings.Trim(strings.TrimSpace(e.Expand(rest)), `"`)
	if target == "" {
		target = "C:"
	}
	e.log("注入自动应答到 " + target)
	panther := filepath.Join(strings.TrimRight(target, "\\")+"\\", "Windows", "Panther")
	if e.dryRun {
		return
	}
	_ = os.MkdirAll(panther, 0o755)
	_ = os.WriteFile(filepath.Join(panther, "unattend.xml"), []byte(unattendXML), 0o644)
}

func (e *Engine) cmdINJECTVNC(rest string) {
	a := splitArgs(e.Expand(rest))
	target := "C:"
	if len(a) > 0 {
		target = a[0]
	}
	hidden := len(a) > 1 && strings.EqualFold(a[1], "TRUE")
	e.log(fmt.Sprintf("注入 VNC 到 %s（隐藏托盘=%v）", target, hidden))
	if e.dryRun {
		return
	}
	flag := "00000001"
	if hidden {
		flag = "00000000"
	}
	reg := strings.Replace(tightVncReg,
		`"RunControlInterface"=dword:00000001`,
		`"RunControlInterface"=dword:`+flag, 1)
	tmp := filepath.Join(os.Getenv("TEMP"), "TightVNC.reg")
	_ = os.WriteFile(tmp, encodeGBK(reg), 0o644)
	e.Run(fmt.Sprintf(`reg import "%s"`, tmp), true, 120)
}

func (e *Engine) cmdINJECTPEDRV(rest string) {
	target := strings.Trim(strings.TrimSpace(e.Expand(rest)), `"`)
	if target == "" {
		target = "C:"
	}
	e.log("注入 PE 驱动到 " + target)
	for _, drv := range []string{"WofAdk.sys", "usbmmidd.sys"} {
		src := filepath.Join(e.client.ExeDir(), "drivers", drv)
		if !fileExistsAny(src) || e.dryRun {
			continue
		}
		dst := filepath.Join(strings.TrimRight(target, "\\")+"\\", "Windows", "System32", "drivers")
		_ = os.MkdirAll(dst, 0o755)
		_ = copyFile(src, filepath.Join(dst, drv))
	}
}

func (e *Engine) cmdUPDATECLIENT(rest string) {
	url := fmt.Sprintf("http://%s/UpData/x64/DPPXEUp.exe", e.client.Server)
	e.log("更新客户端：" + url)
	e.cmdDOWNLOAD(url + " " + e.client.ExeDir())
}

// ---------------------------------------------------------------- 注册表

func regRoot(name string) registry.Key {
	switch strings.ToUpper(name) {
	case "HKCU":
		return registry.CURRENT_USER
	case "HKCR":
		return registry.CLASSES_ROOT
	case "HKU":
		return registry.USERS
	case "HKCC":
		return registry.CURRENT_CONFIG
	default:
		return registry.LOCAL_MACHINE
	}
}

func (e *Engine) regSet(root, sub, name, value string) {
	if e.dryRun {
		return
	}
	k, _, err := registry.CreateKey(regRoot(root), sub, registry.WRITE)
	if err != nil {
		e.log(fmt.Sprintf("写注册表失败 %s\\%s: %v", sub, name, err))
		return
	}
	defer k.Close()
	_ = k.SetStringValue(name, value)
}

func (e *Engine) cmdSETREGSZ(rest string) {
	a := splitArgs(e.Expand(rest))
	if len(a) >= 4 {
		e.regSet(a[0], a[1], a[2], a[3])
	}
}

func (e *Engine) cmdSETREGDWORD(rest string) {
	a := splitArgs(e.Expand(rest))
	if len(a) < 4 {
		return
	}
	v, err := strconv.ParseUint(a[3], 0, 32)
	if err != nil || e.dryRun {
		return
	}
	k, _, err := registry.CreateKey(regRoot(a[0]), a[1], registry.WRITE)
	if err != nil {
		e.log(fmt.Sprintf("写注册表失败 %s\\%s: %v", a[1], a[2], err))
		return
	}
	defer k.Close()
	_ = k.SetDWordValue(a[2], uint32(v))
}

func (e *Engine) cmdGETREGSZ(rest string) {
	a := splitArgs(e.Expand(rest))
	if len(a) < 4 {
		return
	}
	val := ""
	k, err := registry.OpenKey(regRoot(a[1]), a[2], registry.QUERY_VALUE)
	if err == nil {
		defer k.Close()
		if s, _, err := k.GetStringValue(a[3]); err == nil {
			val = s
		} else if n, _, err := k.GetIntegerValue(a[3]); err == nil {
			val = strconv.FormatUint(n, 10)
		}
	}
	e.vars[strings.ToUpper(a[0])] = val
}

func (e *Engine) cmdREGKEYEXISTS(rest string) {
	a := splitArgs(e.Expand(rest))
	if len(a) < 3 {
		return
	}
	ok := "0"
	if k, err := registry.OpenKey(regRoot(a[1]), a[2], registry.QUERY_VALUE); err == nil {
		ok = "1"
		_ = k.Close()
	}
	e.vars[strings.ToUpper(a[0])] = ok
}

func (e *Engine) cmdREGVALUEEXISTS(rest string) {
	a := splitArgs(e.Expand(rest))
	if len(a) < 4 {
		return
	}
	ok := "0"
	if k, err := registry.OpenKey(regRoot(a[1]), a[2], registry.QUERY_VALUE); err == nil {
		if _, _, err := k.GetValue(a[3], nil); err == nil {
			ok = "1"
		}
		_ = k.Close()
	}
	e.vars[strings.ToUpper(a[0])] = ok
}

func (e *Engine) cmdCREATEKEY(rest string) {
	a := splitArgs(e.Expand(rest))
	if len(a) < 2 || e.dryRun {
		return
	}
	k, _, err := registry.CreateKey(regRoot(a[0]), a[1], registry.WRITE)
	if err != nil {
		e.log("创建注册表项失败：" + err.Error())
		return
	}
	_ = k.Close()
}

func (e *Engine) cmdDELREGKEY(rest string) {
	a := splitArgs(e.Expand(rest))
	if len(a) < 2 || e.dryRun {
		return
	}
	if err := registry.DeleteKey(regRoot(a[0]), a[1]); err != nil {
		e.log("删除注册表项失败：" + err.Error())
	}
}

func (e *Engine) cmdDELREGVAL(rest string) {
	a := splitArgs(e.Expand(rest))
	if len(a) < 3 || e.dryRun {
		return
	}
	k, err := registry.OpenKey(regRoot(a[0]), a[1], registry.WRITE)
	if err != nil {
		e.log("删除注册表值失败：" + err.Error())
		return
	}
	defer k.Close()
	if err := k.DeleteValue(a[2]); err != nil {
		e.log("删除注册表值失败：" + err.Error())
	}
}

// ---------------------------------------------------------------- 命令执行

// cmdSHELL 不等待返回，但仍然把输出实时回传（否则界面上什么都看不到）。
func (e *Engine) cmdSHELL(rest string) {
	cmdline := e.Expand(rest)
	if e.dryRun {
		e.log("[模拟] " + cmdline)
		return
	}
	e.log("执行命令：" + cmdline)
	go e.streamOutput(cmdline)
}

func (e *Engine) streamOutput(cmdline string) {
	cmd := exec.Command("cmd", "/c", cmdline)
	hideWindow(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		e.log("执行失败：" + err.Error())
		return
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		e.log("执行失败：" + err.Error())
		return
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(decodeOEM(sc.Bytes()))
		if line != "" {
			e.log("  " + line)
		}
	}
	_ = cmd.Wait()
	e.log(fmt.Sprintf("命令执行完成（返回码 %d）", cmd.ProcessState.ExitCode()))
}

func (e *Engine) cmdSHELLWAIT(rest string) {
	e.log("执行命令 " + rest)
	rc, out := e.Run(rest, true, 3600)
	if out != "" {
		for i, line := range strings.Split(out, "\n") {
			if i >= 40 {
				break
			}
			e.log("  " + strings.TrimRight(line, "\r"))
		}
	}
	e.log(fmt.Sprintf("命令执行完成（返回码 %d）", rc))
}

// ---------------------------------------------------------------- 文件工具

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return nil
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}
