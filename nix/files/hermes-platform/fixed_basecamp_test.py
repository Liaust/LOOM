import json
import os
import unittest
from types import SimpleNamespace
from unittest.mock import patch

import fixed_basecamp


class FixedBasecampTest(unittest.TestCase):
    def setUp(self):
        self.binding = dict(binary="/native/basecamp", home="/personal/home", config_home="/personal/config",
                            cache_home="/personal/cache", profile="codex", identity_id=200003, account_id=300001)
        self.calls = []

    def native_run(self, argv, **kwargs):
        self.calls.append((argv, kwargs))
        return SimpleNamespace(stdout=json.dumps(dict(identity=dict(id=200003),
            accounts=[dict(id=300001, current=True), dict(id=123)])))

    def test_read_and_approved_write_keep_native_cli_coverage(self):
        for command in (["reports", "schedule", "--agent"], ["todos", "create", "Approved task", "--in", "123"]):
            args = ["--profile", "codex", *command]
            argv, env = fixed_basecamp.prepare(self.binding, args, self.native_run)
            self.assertEqual(argv, ["/native/basecamp", *args])
            self.assertEqual(self.calls[-1][0], ["/native/basecamp", "--profile", "codex", "me", "--agent"])
            self.assertEqual(self.calls[-1][1]["cwd"], "/personal/home")
            self.assertEqual(env["BASECAMP_ACCOUNT_ID"], "300001")

    def test_no_ambient_credentials_or_project_or_hermes_context(self):
        with patch.dict(os.environ, BASECAMP_TOKEN="untrusted", BASECAMP_PROJECT_ID="company-project",
                        HERMES_HOME="/company/agent", XDG_CONFIG_HOME="/company/config"):
            _, env = fixed_basecamp.prepare(self.binding, ["--profile", "codex", "me", "--agent"], self.native_run)
        self.assertNotIn("BASECAMP_TOKEN", env)
        self.assertNotIn("BASECAMP_PROJECT_ID", env)
        self.assertNotIn("HERMES_HOME", env)
        self.assertEqual(env["XDG_CONFIG_HOME"], "/personal/config")

    def test_mismatch_never_forwards(self):
        for identity, accounts in ((123, [dict(id=300001, current=True)]),
                                   (200003, [dict(id=300002, current=True)]),
                                   (200003, []),
                                   (200003, [dict(id=300001, current=True)] * 2)):
            with self.subTest(identity=identity, accounts=accounts), self.assertRaisesRegex(ValueError, "mismatch"):
                fixed_basecamp.prepare(self.binding, ["--profile", "codex", "me"],
                    lambda *args, **kwargs: SimpleNamespace(stdout=json.dumps(dict(identity=dict(id=identity), accounts=accounts))))

    def test_selectors_and_auth_lifecycle_refuse_before_preflight(self):
        for args in (["me"], ["--profile", "mina", "me"],
                     ["--profile", "codex", "me", "--account", "300002"],
                     ["--profile", "codex", "me", "--profile=mina"],
                     ["--profile", "codex", "me", "-ja300002"],
                     ["--profile", "codex", "me", "--config-dir=/company"],
                     ["--profile", "codex", "auth", "login"]):
            with self.subTest(args=args), self.assertRaises(ValueError):
                fixed_basecamp.prepare(self.binding, args, self.native_run)
        self.assertEqual(self.calls, [])


if __name__ == "__main__":
    unittest.main()
