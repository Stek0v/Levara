from .LevaraAdapter import LevaraAdapter

# Backwards-compatibility alias for code that still imports the
# pre-rebrand `CognevraAdapter` name. Marked deprecated; remove after
# all consumers migrate to `LevaraAdapter`.
#
# Class-shaped alias (same class object) so isinstance() and subclass
# checks against the legacy name continue to work transparently.
CognevraAdapter = LevaraAdapter

__all__ = ["LevaraAdapter", "CognevraAdapter"]
