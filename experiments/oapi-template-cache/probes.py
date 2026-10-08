"""Repeat the preliminary probes or independent bug reproductions."""
import argparse
import json
import os
from pathlib import Path
import subprocess

SOURCE = Path(__file__).resolve().parent
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("mode", choices=["templates", "bugs"])
parser.add_argument("--work", type=Path, default=Path(os.environ.get("OAPI_EXPERIMENT_WORK", SOURCE / ".work")))
args = parser.parse_args()
base = args.work.resolve()
config = json.loads((base / "experiment.json").read_text())
env = dict(os.environ, GOROOT=config["original_goroot"])
env.pop("OAPI_TREE_CACHE", None)
env.pop("OAPI_TREE_TRACE", None)
records = []


def record(label, command, cwd):
    p = subprocess.run(list(map(str, command)), cwd=cwd, env=env, text=True, capture_output=True)
    records.append({"label": label, "command": list(map(str, command)), "rc": p.returncode,
                    "stdout": p.stdout, "stderr": p.stderr})
    print(label, "rc", p.returncode, p.stdout.strip(), p.stderr.strip(), flush=True)
    return p


if args.mode == "templates":
    templates = base / "src/pkg/codegen/templates"
    for name in ["parse-probe", "copy-probe"]:
        for index in range(5):
            for native in [True, False]:
                command = [base / (name + "-native"), templates] if native else [
                    base / "minigo-bin", "run", ".", "--src", "text/template", "--", templates]
                p = record(f"{name}-{index}-{'native' if native else 'minigo'}", command, base / name)
                if p.returncode:
                    raise SystemExit("Template probe failed")
else:
    for name in ["intsize", "base64"]:
        record(name + "-native", [base / (name + "-native")], base)
        command = [base / "bug-harness-bin", "-C", base / "bugs" / name, "-pkg", ".", "-show-output"]
        if name == "base64":
            command.append("-bind-intsize")
        p = record(name + "-minigo", command, base)
        expected = "undefined: strconv.IntSize" if name == "intsize" else "cannot use slice as []byte"
        if p.returncode == 0 or expected not in p.stdout:
            raise SystemExit("Expected independent bug reproduction did not fail as recorded")
(base / ("probes-" + args.mode + ".json")).write_text(json.dumps(records, indent=2) + "\n")
