"""Build the native oracle, interpreter harnesses and probes in the workspace."""
import argparse
import json
import os
from pathlib import Path
import re
import shutil
import subprocess

SOURCE = Path(__file__).resolve().parent
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--work", type=Path, default=Path(os.environ.get("OAPI_EXPERIMENT_WORK", SOURCE / ".work")))
parser.add_argument("--validate", action="store_true", help="also run modified-stdlib race tests and vet")
args = parser.parse_args()
base = args.work.resolve()
config = json.loads((base / "experiment.json").read_text())
original = Path(config["original_goroot"])
env = dict(os.environ, GOROOT=str(original))
env.setdefault("GOCACHE", str(base / "gocache"))
env.setdefault("GOPROXY", "off")


def run(argv, cwd=base, virtual=False):
    call_env = env.copy()
    if virtual:
        call_env["GOROOT"] = str(base / "goroot")
    print("Running:", " ".join(map(str, argv)), flush=True)
    if argv[0] == "go":
        argv = [original / "bin/go", *argv[1:]]
    subprocess.run(list(map(str, argv)), cwd=cwd, env=call_env, check=True)


run([original / "bin/gofmt", "-w",
     base / "goroot/src/text/template/experiment_cache.go",
     base / "goroot/src/text/template/parse/experiment_snapshot.go"])
for name in ["harness", "bug-harness", "semantic", "copy-probe", "parse-probe", "bugs"]:
    shutil.copytree(SOURCE / name, base / name, dirs_exist_ok=True)
for name in ["harness", "bug-harness"]:
    path = base / name / "go.mod"
    text = re.sub(r"(?m)^replace github.com/podhmo/minigo => .*$",
                  "replace github.com/podhmo/minigo => " + json.dumps(config["minigo"]), path.read_text())
    path.write_text(text)
    run(["go", "build", "-o", base / (name + "-bin"), "."], cwd=base / name)
run(["go", "build", "-ldflags=-X=main.noVCSVersionOverride=v2.0.0-00010101000000-000000000000",
     "-o", base / "oapi-native", "./cmd/oapi-codegen"], cwd=base / "src", virtual=True)
run(["go", "build", "-o", base / "semantic-native", "."], cwd=base / "semantic", virtual=True)
for name in ["copy-probe", "parse-probe"]:
    run(["go", "build", "-o", base / (name + "-native"), "."], cwd=base / name)
run(["go", "build", "-o", base / "minigo-bin", "./cmd/minigo"], cwd=Path(config["minigo"]))
for name in ["base64", "intsize"]:
    run(["go", "build", "-o", base / (name + "-native"), "."], cwd=base / "bugs" / name)
if args.validate:
    run(["go", "test", "-race", "text/template/parse", "text/template"], virtual=True)
    run(["go", "vet", "text/template/parse", "text/template"], virtual=True)
    run(["go", "vet", "./..."], cwd=base / "harness")
