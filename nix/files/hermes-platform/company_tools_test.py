import json
import unittest
from types import SimpleNamespace

import company_tools


class CompanyToolsTest(unittest.TestCase):
    def setUp(self):
        self.context = dict(environment={key: "/company/" + key.lower() for key in ("HOME", "PATH", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "GH_CONFIG_DIR", "GIT_CONFIG_GLOBAL", "GIT_SSH_COMMAND")}, commands=dict(basecamp="/native/basecamp", gh="/native/gh"), basecamp=None, github_host=None)
        self.context["environment"].update(LOOM_BOX_ID="box_company", LOOM_BOX_KEY="company", LOOM_CONFIG_FILE="/etc/loom-client.env")

    def test_unknown_company_auth_is_unavailable(self):
        for tool in ("basecamp", "gh"):
            with self.subTest(tool=tool), self.assertRaises(ValueError):
                company_tools.prepare(self.context, tool, ["--help"])

    def test_full_native_gh_commands_are_forwarded(self):
        self.context["github"] = dict(host="github.com", login="company-human", id=123)
        self.context["environment"]["PATH"] = "/company/bin:/etc/profiles/per-user/agents/bin:/home/agents/.local/bin:/run/current-system/sw/bin"
        args = ["repo", "create", "approved-org/repository", "--private", "--description", "Company repository"]
        argv, env = company_tools.prepare(self.context, "gh", args, lambda *a, **kw: SimpleNamespace(stdout=json.dumps(dict(login="company-human", id=123))))
        self.assertEqual(argv, ["/native/gh", *args])
        self.assertEqual(env["GH_HOST"], "github.com")
        self.assertNotIn("GH_TOKEN", env)
        self.assertNotIn("SSH_AUTH_SOCK", env)
        self.assertEqual(env["PATH"], "/company/bin:/run/current-system/sw/bin")
        self.assertEqual(env["LOOM_BOX_ID"], "box_company")
        self.assertEqual(env["LOOM_BOX_KEY"], "company")
        self.assertEqual(env["LOOM_CONFIG_FILE"], "/etc/loom-client.env")

    def test_gh_wrong_actor_and_host_refuse_without_global_env_changes(self):
        self.context["github"] = dict(host="github.com", login="company-human", id=123)
        before = dict(company_tools.os.environ)
        for args, actor in [(["api", "user"], dict(login="personal", id=123)),
                            (["api", "user", "--hostname=other.example"], dict(login="company-human", id=123))]:
            with self.subTest(args=args), self.assertRaises(ValueError):
                company_tools.prepare(self.context, "gh", args, lambda *a, **kw: SimpleNamespace(stdout=json.dumps(actor)))
        self.assertEqual(before, dict(company_tools.os.environ))

    def test_company_basecamp_preflight_and_full_native_command(self):
        self.context["basecamp"] = dict(profile="company-human", identity_id=123, account_id=789)
        calls = []
        def run(argv, **kwargs):
            calls.append((argv, kwargs))
            return SimpleNamespace(stdout=json.dumps(dict(identity=dict(id=123), accounts=[dict(id=789, current=True)])))
        args = ["--profile", "company-human", "projects", "create", "Approved company project"]
        argv, env = company_tools.prepare(self.context, "basecamp", args, run)
        self.assertEqual(argv, ["/native/basecamp", *args])
        self.assertEqual(calls[0][0], ["/native/basecamp", "--profile", "company-human", "me", "--agent"])
        self.assertEqual(env["BASECAMP_ACCOUNT_ID"], "789")
        with self.assertRaises(ValueError):
            company_tools.prepare(self.context, "basecamp", ["projects", "--profile=mina"], run)

    def test_mismatch_or_explicitly_forbidden_account_binding_refuses(self):
        self.context["basecamp"] = dict(profile="company-human", identity_id=123, account_id=789)
        with self.assertRaisesRegex(ValueError, "mismatch"):
            company_tools.prepare(self.context, "basecamp", ["projects"], lambda *a, **kw: SimpleNamespace(stdout=json.dumps(dict(identity=dict(id=123), accounts=[dict(id=100, current=True)]))))
        self.context["basecamp"]["account_id"] = 300001
        self.context["forbidden_basecamp_account_ids"] = [300001]
        with self.assertRaisesRegex(ValueError, "unavailable"):
            company_tools.prepare(self.context, "basecamp", ["projects"])


if __name__ == "__main__":
    unittest.main()
