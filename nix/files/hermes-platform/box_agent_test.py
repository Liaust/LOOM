import contextlib
import io
import json
import os
import sys
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import box_agent
import yaml


class BootstrapEntrypointTest(unittest.TestCase):
    def test_shell_context_upgrade_preserves_learned_personality_and_refuses_live_config_edits(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory).resolve();native=root/'native/.hermes';native.mkdir(parents=True)
            box=root/'company';box.mkdir();templates=root/'templates';templates.mkdir()
            for name in ('SOUL.md','USER.md','AGENTS.md'):(templates/name).write_text(name+' bootstrap\n')
            profile=native/'profiles/tyler';launcher=root/'launcher.py';launcher.write_text('')
            commands={'basecamp':'/bin/true','gh':'/bin/true'}
            args=(native,{'root_path':str(box),'box_id':'box_company'},'company','tyler',templates,'gpt-6.1-sol','openai-codex',sys.executable,launcher,'/bin',commands,'openai-codex')
            def create(*args,**kwargs):
                (profile/'memories').mkdir(parents=True)
                return profile
            with patch.dict(os.environ,{'HERMES_HOME':str(native)}),patch('hermes_cli.profiles.create_profile',side_effect=create),patch('hermes_cli.profiles.write_profile_meta'):
                box_agent.prepare_profile(*args)
                config_path=profile/'config.yaml';config=yaml.safe_load(config_path.read_text())
                self.assertIs(config['terminal']['auto_source_bashrc'],False)
                shell=(profile/'home/.bash_profile').read_text()
                self.assertIn(str(profile/'home/bin'),shell)
                self.assertEqual((profile/'home/.bashrc').read_text(),shell)
                (profile/'SOUL.md').write_text('Learned personality, retain me\n')
                del config['terminal']['auto_source_bashrc'];config_path.write_text(yaml.safe_dump(config,sort_keys=False))
                fields=dict(config=config,soul=(templates/'SOUL.md').read_text(),user=(templates/'USER.md').read_text(),agents=(templates/'AGENTS.md').read_text(),commands=commands,python=sys.executable,launcher=str(launcher),shell_startup=shell)
                marker_path=profile/box_agent.MARKER;marker=json.loads(marker_path.read_text())
                marker['context_revision']=box_agent.digest(json.dumps(fields,sort_keys=True).encode())
                marker['bootstrap_hashes'][str(config_path)]=box_agent.digest(config_path.read_bytes())
                marker_path.write_text(json.dumps(marker))
                config_path.write_text(config_path.read_text()+'# user edit\n')
                with self.assertRaisesRegex(ValueError,'live native configuration changed'):box_agent.prepare_profile(*args)
                config_path.write_text(yaml.safe_dump(config,sort_keys=False))
                result=box_agent.prepare_profile(*args)
                self.assertIn(str(profile/'SOUL.md'),result['drift'])
                self.assertEqual((profile/'SOUL.md').read_text(),'Learned personality, retain me\n')
                self.assertIs(yaml.safe_load(config_path.read_text())['terminal']['auto_source_bashrc'],False)
                self.assertEqual((profile/'home/.bash_profile').read_text(),shell)

    def test_cli_binds_native_creation_catalog_before_calling_prepare(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory)
            commands=root/'commands.json'
            commands.write_text('{}')
            args=['prepare','--native-home',str(root/'served/.hermes'),
                  '--boxes-file',str(root/'catalog.json'),'--templates',str(root/'templates'),
                  '--tool-launcher',str(root/'launcher'),'--external-contexts',str(root/'contexts.json'),
                  '--model','gpt-6.1-sol','--provider','openai-codex','--python','/python',
                  '--tool-path','/bin','--commands-file',str(commands)]
            def prepare(native_home,*args,**kwargs):
                self.assertEqual(os.environ['HERMES_HOME'],str(native_home))
                return {'status':'checked'}
            with patch.dict(os.environ,{'HERMES_HOME':'/wrong/catalog'}),patch('sys.argv',args),patch.object(box_agent,'prepare',side_effect=prepare),contextlib.redirect_stdout(io.StringIO()) as output:
                box_agent.main()
            self.assertEqual(json.loads(output.getvalue()),{'status':'checked'})


if __name__=='__main__':
    unittest.main()
