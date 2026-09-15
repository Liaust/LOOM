"""W5's missing F10 process proof, using the unchanged public endpoint helpers.

This exercises real synthetic MCP subprocesses; it is not a live Cua session,
GUI, Linux namespace, gateway, TUI or model test.
"""
import unittest
import test_mac_endpoint as endpoint_tests


class SessionLifetimeAcceptance(endpoint_tests.EndpointTests):
    async def test_ending_one_session_keeps_other_session_usable(self):
        one, _ = await self.ready()
        two, _ = await self.ready()
        await self.capture(one)
        await self.capture(two)
        starts = [c['arguments']['session'] for c in self.h.calls() if c['name'] == 'start_session']
        self.assertEqual(len(starts), 2)
        self.assertNotEqual(starts[0], starts[1])
        # A caller-supplied session cannot select the other endpoint's session.
        ended = await self.call(one, 'tools/call', {
            'name': 'end_session', 'arguments': {'session': starts[1]}})
        self.assertFalse(ended['result']['isError'])
        self.assertEqual([c['arguments']['session'] for c in self.h.calls()
                          if c['name'] == 'end_session'], [starts[0]])
        await one.close()
        self.assertIsNotNone(one.process.returncode)
        self.assertIsNone(two.process.returncode)
        await self.capture(two)
        result = await self.call(two, 'tools/call', self.click_args())
        self.assertEqual(result['result']['structuredContent']['delivery'], 'confirmed')
        clicks = [c for c in self.h.calls() if c['name'] == 'click']
        self.assertEqual(len(clicks), 1)
        self.assertEqual(clicks[0]['arguments']['session'], starts[1])
        self.assertEqual([c['arguments']['session'] for c in self.h.calls()
                          if c['name'] == 'start_session'], starts)
        self.assertFalse(any(c['name'] in {'stop', 'serve', 'set_config'} for c in self.h.calls()))
        # The inherited asyncTearDown also proves every fixture child exits.


def load_tests(loader, tests, pattern):
    # Reuse setup/teardown and helpers without re-running inherited test methods.
    return unittest.TestSuite([SessionLifetimeAcceptance(
        'test_ending_one_session_keeps_other_session_usable')])


if __name__ == '__main__':
    unittest.main()
