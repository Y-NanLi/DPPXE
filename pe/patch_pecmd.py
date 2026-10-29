"""给 PE 的 Pecmd.ini 追加"开机启动被控端"的一行（保持 GBK 编码与行尾风格）。"""

import io
import sys

SRC = r"D:/wb/h-go/go/pe/_orig/Pecmd.ini"
DST = r"D:/wb/h-go/go/pe/Pecmd.ini"

raw = io.open(SRC, "rb").read()
txt = raw.decode("gbk", "replace")
crlf = "\r\n" in txt
nl = "\r\n" if crlf else "\n"

lines = [
    "",
    "//===== DP-PXE 被控端开机自启 =====",
    r"EXEC %SystemRoot%\System32\cmd.exe /c X:\DPPXE\start_client.cmd",
    "",
]
if not txt.endswith(("\n", "\r")):
    txt += nl
txt += nl.join(lines)

out = txt.encode("gbk", "replace")
io.open(DST, "wb").write(out)
print("written:", DST, len(out), "bytes, CRLF =", crlf)

# 回读最后几行确认
back = io.open(DST, "rb").read().decode("gbk", "replace")
print("--- tail ---")
for line in back.splitlines()[-6:]:
    print(line)
