"""Tests for secret reference resolution."""

import pytest
from src.secrets_resolver import (
    SecretRef,
    parse_secret_ref,
    find_secret_refs,
    is_secret_ref,
    has_secret_refs,
    resolve_secrets_in_string,
    resolve_secrets_in_dict,
    collect_secret_refs,
)


class TestSecretRefParsing:
    """Tests for parsing secret references."""

    def test_parse_simple_ref(self):
        """Test parsing a simple secret reference."""
        ref = parse_secret_ref("${secret:myproject:API_KEY}")
        assert ref is not None
        assert ref.path == "myproject"
        assert ref.key == "API_KEY"
        assert ref.raw == "${secret:myproject:API_KEY}"

    def test_parse_ref_with_slashes(self):
        """Test parsing a reference with slashes in path."""
        ref = parse_secret_ref("${secret:org/project/prod:DB_PASSWORD}")
        assert ref is not None
        assert ref.path == "org/project/prod"
        assert ref.key == "DB_PASSWORD"

    def test_parse_ref_with_dashes(self):
        """Test parsing a reference with dashes."""
        ref = parse_secret_ref("${secret:npm-publishing/tokens:NPM_TOKEN}")
        assert ref is not None
        assert ref.path == "npm-publishing/tokens"
        assert ref.key == "NPM_TOKEN"

    def test_parse_ref_with_underscores(self):
        """Test parsing a reference with underscores."""
        ref = parse_secret_ref("${secret:my_project/prod_env:api_key}")
        assert ref is not None
        assert ref.path == "my_project/prod_env"
        assert ref.key == "api_key"

    def test_parse_invalid_returns_none(self):
        """Test that invalid strings return None."""
        invalid = [
            "not a ref",
            "${secret:}",
            "${secret:path:}",
            "${secret::key}",
            "${notasecret:path:key}",
            "${secret:path}",  # Missing key
            "${secret:pa th:key}",  # Space in path
            "${secret:path:ke y}",  # Space in key
            "${secret:path:key/with/slash}",  # Slash in key
        ]
        for s in invalid:
            assert parse_secret_ref(s) is None, f"Expected None for: {s}"

    def test_is_secret_ref(self):
        """Test checking if string is a secret ref."""
        assert is_secret_ref("${secret:path:key}") is True
        assert is_secret_ref("${secret:a/b/c:key}") is True
        assert is_secret_ref("not a ref") is False
        assert is_secret_ref("prefix ${secret:p:k} suffix") is False  # Not full match

    def test_has_secret_refs(self):
        """Test checking if string contains secret refs."""
        assert has_secret_refs("Use ${secret:p:k} here") is True
        assert has_secret_refs("${secret:a:b} and ${secret:c:d}") is True
        assert has_secret_refs("no secrets here") is False
        assert has_secret_refs("${notasecret:a:b}") is False

    def test_find_secret_refs_single(self):
        """Test finding a single secret reference in a string."""
        text = "Token: ${secret:auth:TOKEN}"
        refs = find_secret_refs(text)

        assert len(refs) == 1
        assert refs[0].path == "auth"
        assert refs[0].key == "TOKEN"
        assert refs[0].raw == "${secret:auth:TOKEN}"

    def test_find_secret_refs_multiple(self):
        """Test finding multiple secret references in a string."""
        text = "Token: ${secret:auth:TOKEN}, Key: ${secret:api/prod:API_KEY}"
        refs = find_secret_refs(text)

        assert len(refs) == 2
        assert refs[0].path == "auth"
        assert refs[0].key == "TOKEN"
        assert refs[1].path == "api/prod"
        assert refs[1].key == "API_KEY"

    def test_find_secret_refs_none(self):
        """Test finding no secret references."""
        text = "No secrets here at all"
        refs = find_secret_refs(text)
        assert refs == []

    def test_secret_ref_is_frozen(self):
        """Test that SecretRef is immutable."""
        ref = SecretRef(path="p", key="k", raw="${secret:p:k}")
        with pytest.raises(AttributeError):
            ref.path = "new"


class TestSecretResolution:
    """Tests for resolving secret references."""

    def test_resolve_simple_string(self):
        """Test resolving a simple string with secret reference."""
        def get_secret(path, key):
            if path == "project" and key == "TOKEN":
                return "secret-value"
            return None

        result = resolve_secrets_in_string(
            "Token: ${secret:project:TOKEN}",
            get_secret
        )
        assert result == "Token: secret-value"

    def test_resolve_multiple_refs(self):
        """Test resolving multiple references in one string."""
        secrets = {
            ("a", "K1"): "v1",
            ("b", "K2"): "v2",
        }
        def get_secret(path, key):
            return secrets.get((path, key))

        result = resolve_secrets_in_string(
            "${secret:a:K1} and ${secret:b:K2}",
            get_secret
        )
        assert result == "v1 and v2"

    def test_resolve_same_ref_multiple_times(self):
        """Test resolving the same reference appearing multiple times."""
        def get_secret(path, key):
            return "value"

        result = resolve_secrets_in_string(
            "${secret:p:k} and ${secret:p:k}",
            get_secret
        )
        assert result == "value and value"

    def test_resolve_no_refs(self):
        """Test resolving a string with no references."""
        def get_secret(path, key):
            return "value"

        result = resolve_secrets_in_string(
            "No secrets here",
            get_secret
        )
        assert result == "No secrets here"

    def test_resolve_missing_raises(self):
        """Test that missing secrets raise ValueError."""
        def get_secret(path, key):
            return None

        with pytest.raises(ValueError, match="Secret not found"):
            resolve_secrets_in_string("${secret:missing:KEY}", get_secret)

    def test_resolve_missing_ok(self):
        """Test that missing_ok leaves refs unreplaced."""
        def get_secret(path, key):
            return None

        result = resolve_secrets_in_string(
            "${secret:missing:KEY}",
            get_secret,
            missing_ok=True
        )
        assert result == "${secret:missing:KEY}"

    def test_resolve_partial_missing_ok(self):
        """Test partial resolution with missing_ok."""
        def get_secret(path, key):
            if path == "found":
                return "resolved"
            return None

        result = resolve_secrets_in_string(
            "${secret:found:KEY} and ${secret:missing:KEY}",
            get_secret,
            missing_ok=True
        )
        assert result == "resolved and ${secret:missing:KEY}"

    def test_resolve_dict_simple(self):
        """Test resolving secrets in a simple dictionary."""
        def get_secret(path, key):
            return f"resolved-{path}-{key}"

        data = {
            "token": "${secret:auth:TOKEN}",
            "normal": "no secrets here",
        }

        result = resolve_secrets_in_dict(data, get_secret)

        assert result["token"] == "resolved-auth-TOKEN"
        assert result["normal"] == "no secrets here"

    def test_resolve_dict_nested(self):
        """Test resolving secrets in nested dictionaries."""
        def get_secret(path, key):
            return f"resolved-{path}-{key}"

        data = {
            "token": "${secret:auth:TOKEN}",
            "nested": {
                "key": "${secret:api:KEY}",
                "deep": {
                    "secret": "${secret:db:PASSWORD}"
                }
            }
        }

        result = resolve_secrets_in_dict(data, get_secret)

        assert result["token"] == "resolved-auth-TOKEN"
        assert result["nested"]["key"] == "resolved-api-KEY"
        assert result["nested"]["deep"]["secret"] == "resolved-db-PASSWORD"

    def test_resolve_dict_with_list(self):
        """Test resolving secrets in dictionary with lists."""
        def get_secret(path, key):
            return f"resolved-{path}-{key}"

        data = {
            "tokens": ["${secret:a:T1}", "plain", "${secret:b:T2}"],
            "normal": ["no", "secrets"]
        }

        result = resolve_secrets_in_dict(data, get_secret)

        assert result["tokens"] == ["resolved-a-T1", "plain", "resolved-b-T2"]
        assert result["normal"] == ["no", "secrets"]

    def test_resolve_dict_non_string_values(self):
        """Test that non-string values are preserved."""
        def get_secret(path, key):
            return "value"

        data = {
            "string": "${secret:p:k}",
            "number": 42,
            "boolean": True,
            "null": None,
            "float": 3.14,
        }

        result = resolve_secrets_in_dict(data, get_secret)

        assert result["string"] == "value"
        assert result["number"] == 42
        assert result["boolean"] is True
        assert result["null"] is None
        assert result["float"] == 3.14

    def test_resolve_dict_empty(self):
        """Test resolving an empty dictionary."""
        def get_secret(path, key):
            return "value"

        result = resolve_secrets_in_dict({}, get_secret)
        assert result == {}


class TestCollectSecretRefs:
    """Tests for collecting secret references."""

    def test_collect_from_simple_dict(self):
        """Test collecting refs from a simple dictionary."""
        data = {
            "a": "${secret:p1:k1}",
            "b": "no secret",
        }

        refs = collect_secret_refs(data)
        paths = [(r.path, r.key) for r in refs]

        assert ("p1", "k1") in paths
        assert len(refs) == 1

    def test_collect_from_nested_dict(self):
        """Test collecting refs from nested dictionaries."""
        data = {
            "a": "${secret:p1:k1}",
            "nested": {
                "c": "${secret:p2:k2}"
            }
        }

        refs = collect_secret_refs(data)
        paths = [(r.path, r.key) for r in refs]

        assert ("p1", "k1") in paths
        assert ("p2", "k2") in paths
        assert len(refs) == 2

    def test_collect_from_dict_with_lists(self):
        """Test collecting refs from dictionary with lists."""
        data = {
            "a": "${secret:p1:k1}",
            "list": ["${secret:p2:k2}", "plain", "${secret:p3:k3}"]
        }

        refs = collect_secret_refs(data)
        paths = [(r.path, r.key) for r in refs]

        assert ("p1", "k1") in paths
        assert ("p2", "k2") in paths
        assert ("p3", "k3") in paths
        assert len(refs) == 3

    def test_collect_no_refs(self):
        """Test collecting from dictionary with no refs."""
        data = {
            "a": "plain",
            "b": "also plain",
        }

        refs = collect_secret_refs(data)
        assert refs == []

    def test_collect_multiple_refs_in_value(self):
        """Test collecting multiple refs from a single value."""
        data = {
            "combined": "${secret:p1:k1} and ${secret:p2:k2}",
        }

        refs = collect_secret_refs(data)
        paths = [(r.path, r.key) for r in refs]

        assert ("p1", "k1") in paths
        assert ("p2", "k2") in paths
        assert len(refs) == 2

    def test_collect_empty_dict(self):
        """Test collecting from empty dictionary."""
        refs = collect_secret_refs({})
        assert refs == []
