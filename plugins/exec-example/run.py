#!/usr/bin/env python3
"""
Exec-авторег пример: любой язык, нож по маслу.
Протокол: stdin -> {"config": {...}, "options": {"count":2}}
          stdout <- [{"token_v2":"...","label":"..."}]
"""
import json, sys
def main():
    req = json.load(sys.stdin)
    cfg = req.get("config") or {}
    opts = req.get("options") or {}
    count = int(opts.get("count", 1))
    prefix = opts.get("label_prefix") or cfg.get("prefix") or "exec"
    # TODO: твой реальный флоу — создай почту, реши капчу, зарегай Notion, достань token_v2
    out = []
    for i in range(count):
        out.append({
            "token_v2": f"v02:mock-exec-{prefix}-{i+1}",
            "label": f"{prefix}-{i+1}"
        })
    json.dump(out, sys.stdout)
if __name__ == "__main__":
    main()
