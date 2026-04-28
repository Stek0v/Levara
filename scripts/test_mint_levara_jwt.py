import json
import os
import stat
import subprocess
import tempfile
import textwrap
import unittest
from pathlib import Path


class MintLevaraJWTTest(unittest.TestCase):
    def test_register_and_login_use_same_service_email(self):
        repo_root = Path(__file__).resolve().parents[1]
        script = repo_root / "scripts" / "mint-levara-jwt.sh"

        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            bin_dir = tmp_path / "bin"
            bin_dir.mkdir()

            (bin_dir / "openssl").write_text("#!/bin/sh\necho fixed-service-password\n")
            (bin_dir / "curl").write_text(
                textwrap.dedent(
                    f"""\
                    #!/bin/sh
                    payload=""
                    prev=""
                    for arg in "$@"; do
                      if [ "$prev" = "-d" ]; then
                        payload="$arg"
                      fi
                      prev="$arg"
                    done
                    case "$*" in
                      *"/auth/register"*)
                        printf '%s' "$payload" > "{tmp_path}/register.json"
                        exit 0
                        ;;
                      *"/auth/login"*)
                        printf '%s' "$payload" > "{tmp_path}/login.json"
                        printf '{{"access_token":"service-token","token_type":"bearer"}}'
                        exit 0
                        ;;
                    esac
                    echo "unexpected curl call: $*" >&2
                    exit 2
                    """
                )
            )
            for tool in ("openssl", "curl"):
                path = bin_dir / tool
                path.chmod(path.stat().st_mode | stat.S_IXUSR)

            env = os.environ.copy()
            env["PATH"] = f"{bin_dir}:{env['PATH']}"
            env["HOME"] = str(tmp_path / "home")
            env["LEVARA_URL"] = "http://levara.local:8080"

            out = subprocess.check_output(
                ["bash", str(script), "picoclaw"],
                cwd=repo_root,
                env=env,
                text=True,
            )

            self.assertEqual(out.strip(), "service-token")
            register = json.loads((tmp_path / "register.json").read_text())
            login = json.loads((tmp_path / "login.json").read_text())
            self.assertEqual(register["email"], "picoclaw@service.local")
            self.assertEqual(login["username"], "picoclaw@service.local")


if __name__ == "__main__":
    unittest.main()
