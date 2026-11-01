import io
import json
import unittest
from unittest.mock import patch

import workload_run


class ResourceProbeTests(unittest.TestCase):
    def test_repeated_probes_do_not_replay_cached_exec_results(self):
        executions = {}
        def execute(request, timeout):
            name = json.loads(request.data)['name']
            executions.setdefault(name, len(executions) + 1)
            return io.BytesIO(json.dumps({'exit_code':0, 'stdout':json.dumps({'at':executions[name]})}).encode())
        with patch('workload_run.m.read', return_value={'agent37_key_file':'unused'}), \
             patch('workload_run.Path.read_text', return_value='test-key'), \
             patch('workload_run.time.time_ns', side_effect=[1,2]), \
             patch('workload_run.urllib.request.urlopen', side_effect=execute):
            first = workload_run.probe('test-vm')
            second = workload_run.probe('test-vm')
        self.assertGreater(second['at'], first['at'])


if __name__ == '__main__':
    unittest.main()
