from pathlib import Path

p = Path("/etc/samba/smb.conf")
s = p.read_text()

if "[global]" not in s:
    s = "[global]\n" + s

lines = s.splitlines()
out = []
in_global = False
written = {
    "server min protocol": False,
    "client min protocol": False,
    "server max protocol": False,
}

for line in lines:
    stripped = line.strip()
    if stripped.startswith("[") and stripped.endswith("]"):
        if in_global:
            for k, v in written.items():
                if not v:
                    val = "SMB2_02" if k != "server max protocol" else "SMB3"
                    out.append(f"   {k} = {val}")
        in_global = stripped.lower() == "[global]"
        out.append(line)
        continue

    if in_global:
        key = stripped.split("=", 1)[0].strip().lower() if "=" in stripped else ""
        if key in written:
            val = "SMB2_02" if key != "server max protocol" else "SMB3"
            out.append(f"   {key} = {val}")
            written[key] = True
            continue

    out.append(line)

if in_global:
    for k, v in written.items():
        if not v:
            val = "SMB2_02" if k != "server max protocol" else "SMB3"
            out.append(f"   {k} = {val}")

p.write_text("\n".join(out) + "\n")
