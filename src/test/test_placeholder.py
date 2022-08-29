import unittest


class Testing(unittest.TestCase):
    def test_string(self) -> None:
        a = "some"
        b = "some"
        self.assertEqual(a, b)

    def test_boolean(self) -> None:
        a = True
        b = True
        self.assertEqual(a, b)


if __name__ == "__main__":
    unittest.main()
