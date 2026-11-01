import unittest
from unittest.mock import patch

import integration


class JobPaginationTests(unittest.TestCase):
    def test_server_can_return_fewer_jobs_than_requested(self):
        jobs = [{'id': index} for index in range(90)]
        with patch('integration.m.gitea', side_effect=[{'total_count':90,'jobs':jobs[:50]}, {'total_count':90,'jobs':jobs[50:]}]) as request:
            self.assertEqual(integration.run_jobs(1), jobs)
        self.assertTrue(request.call_args.args[1].endswith('page=2'))

    def test_repeated_page_is_rejected(self):
        with patch('integration.m.gitea', return_value={'total_count':90,'jobs':[{'id':1}]}):
            with self.assertRaisesRegex(RuntimeError, 'did not advance'):
                integration.run_jobs(1)
