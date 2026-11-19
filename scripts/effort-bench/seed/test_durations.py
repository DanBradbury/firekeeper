import unittest

from durations import format_duration, parse_duration


class ParseTest(unittest.TestCase):
    def test_single_units(self):
        self.assertEqual(parse_duration("45s"), 45)
        self.assertEqual(parse_duration("3m"), 180)
        self.assertEqual(parse_duration("2h"), 7200)
        self.assertEqual(parse_duration("1d"), 86400)

    def test_combined(self):
        self.assertEqual(parse_duration("1h30m"), 5400)
        self.assertEqual(parse_duration("2d4h"), 187200)
        self.assertEqual(parse_duration("1d2h3m4s"), 93784)

    def test_zero_parts(self):
        self.assertEqual(parse_duration("0s"), 0)
        self.assertEqual(parse_duration("0h5m"), 300)

    def test_invalid(self):
        for bad in ["", "5", "h", "1x", "1h1h", "1m1h", "1 h", "-1s", "1.5h"]:
            with self.subTest(bad=bad):
                with self.assertRaises(ValueError):
                    parse_duration(bad)


class FormatTest(unittest.TestCase):
    def test_format(self):
        self.assertEqual(format_duration(0), "0s")
        self.assertEqual(format_duration(45), "45s")
        self.assertEqual(format_duration(5400), "1h30m")
        self.assertEqual(format_duration(93784), "1d2h3m4s")
        self.assertEqual(format_duration(86400), "1d")

    def test_negative(self):
        with self.assertRaises(ValueError):
            format_duration(-1)

    def test_round_trip(self):
        for n in [0, 1, 59, 60, 3599, 3600, 86399, 86401, 987654]:
            with self.subTest(n=n):
                self.assertEqual(parse_duration(format_duration(n)), n)


if __name__ == "__main__":
    unittest.main()
