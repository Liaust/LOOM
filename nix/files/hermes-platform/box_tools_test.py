import json
import os
import unittest
from types import SimpleNamespace

import box_tools


class BoxToolsTest(unittest.TestCase):
    def setUp(self):
        self.native = dict(basecamp="/native/basecamp", gh="/native/gh")
        self.context = dict(box_key="company", box_id="box_company", policy_status="available")
        self.bindings = dict(company=dict(HOME="/company/home"))
        env = {key: "/company/" + key.lower() for key in (
            "HOME", "PATH", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME",
            "GH_CONFIG_DIR", "GIT_CONFIG_GLOBAL", "GIT_SSH_COMMAND")}
        env.update(HOME="/company/home", LOOM_BOX_ID="box_company", LOOM_BOX_KEY="company", HERMES_HOME="/company/profile")
        self.tools = dict(environment=env, commands=self.native,
                          basecamp=None, github=dict(host="github.com", login="company", id=12))

    def test_personal_missing_binding_never_uses_ambient_authentication(self):
        with unittest.mock.patch.dict(os.environ, BASECAMP_TOKEN="ambient", GH_TOKEN="ambient"):
            with self.assertRaisesRegex(ValueError, "binding unavailable"):
                box_tools.dispatch(dict(box_key="personal", policy_status="available"), {}, self.native,
                                   "basecamp", ["--profile", "codex", "me", "--agent"])

    def test_personal_uses_verified_explicit_codex_binding(self):
        context = dict(box_key="personal", box_id="box_personal", policy_status="available")
        env = dict(self.tools["environment"], LOOM_BOX_ID="box_personal", LOOM_BOX_KEY="personal")
        binding = dict(profile="codex", identity_id=21, account_id=31)
        tools = dict(self.tools, environment=env, basecamp=binding)
        def run(*args, **kwargs):
            self.assertEqual(args[0], ["/native/basecamp", "--profile", "codex", "me", "--agent"])
            self.assertNotIn("BASECAMP_TOKEN", kwargs["env"])
            return SimpleNamespace(stdout=json.dumps(dict(identity=dict(id=21), accounts=[dict(id=31, current=True)])))
        argv, selected = box_tools.dispatch(context, dict(personal=dict(HOME="/company/home")), self.native,
                                           "basecamp", ["--profile", "codex", "todos", "list"],
                                           load=lambda path: tools, run=run)
        self.assertEqual(argv, ["/native/basecamp", "--profile", "codex", "todos", "list"])
        self.assertEqual(selected["BASECAMP_ACCOUNT_ID"], "31")
        self.assertNotIn("HERMES_HOME", selected)

    def test_company_shares_only_account_context_not_identity(self):
        before = dict(os.environ)
        argv, env = box_tools.dispatch(self.context, self.bindings, self.native, "gh", ["repo", "create", "org/repo"],
                                      load=lambda path: self.tools,
                                      run=lambda *a, **kw: SimpleNamespace(stdout=json.dumps(dict(login="company", id=12))))
        self.assertEqual(argv, ["/native/gh", "repo", "create", "org/repo"])
        self.assertNotIn("HERMES_HOME", env)
        self.assertNotIn("CODEX_HOME", env)
        self.assertNotIn("GH_TOKEN", env)
        self.assertEqual(env["HOME"], "/company/home")
        self.assertEqual(dict(os.environ), before)
        self.assertIn("HERMES_HOME", self.tools["environment"])

    def test_missing_or_mismatched_context_never_falls_back(self):
        with self.assertRaises(ValueError):
            box_tools.dispatch(dict(self.context, policy_status="missing"), self.bindings, self.native, "gh", ["--help"])
        with self.assertRaises(ValueError):
            box_tools.dispatch(self.context, {}, self.native, "gh", ["--help"])
        self.tools["environment"]["LOOM_BOX_ID"] = "box_personal"
        with self.assertRaises(ValueError):
            box_tools.dispatch(self.context, self.bindings, self.native, "gh", ["--help"], load=lambda p: self.tools)

    def test_explicit_context_is_passed_to_existing_resolver(self):
        calls = []
        def run(argv, **kwargs):
            calls.append(argv)
            return SimpleNamespace(stdout=json.dumps(dict(ok=True, data=self.context)))
        self.assertEqual(box_tools.resolve("/native/loom", "box_company", run), self.context)
        self.assertEqual(calls, [["/native/loom", "box", "context", "--json", "--box", "box_company"]])


if __name__ == "__main__":
    unittest.main()
