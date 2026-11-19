"""Small duration helpers. The benchmark asks the agent to implement both."""


def parse_duration(text: str) -> int:
    """Return the number of seconds in a duration such as "1h30m", "45s" or "2d4h".

    Units are d, h, m and s, each at most once and in that order. No spaces.
    Raise ValueError for an empty string, an unknown unit, a unit with no
    number, a number with no unit, a repeated unit or units out of order.
    """
    raise NotImplementedError


def format_duration(seconds: int) -> str:
    """Inverse of parse_duration: largest units first, zero parts left out.

    format_duration(0) is "0s". Raise ValueError for a negative number.
    """
    raise NotImplementedError
