//! Registered secrets, the built-in secret scan, and the decode depth.
//!
//! The scan removes each registered secret from the data: the raw value
//! and its common encodings (base64, base64url, hex, percent encoding). It
//! also decodes each base64 or hex run that holds text (or gzip), and scans
//! the decoded text again. A run that holds a secret becomes
//! "[redacted:<id>]". The depth is the number of nested encodings found.

use std::collections::HashMap;
use std::io::Read as _;
use std::path::Path;
use std::sync::LazyLock;

use base64::Engine as _;
use base64::engine::general_purpose::{GeneralPurpose, GeneralPurposeConfig, STANDARD, STANDARD_NO_PAD, URL_SAFE, URL_SAFE_NO_PAD};
use base64::engine::DecodePaddingMode;
use regex::bytes::Regex;

/// A secret shorter than this is not scanned: it would match normal text.
pub const MIN_SECRET: usize = 8;

/// A run of base64 or hex characters that can hold encoded data.
static RUN: LazyLock<Regex> = LazyLock::new(|| Regex::new(r"[A-Za-z0-9+/_-]{24,}={0,2}").unwrap());
/// Placeholders that a job has in place of secrets.
pub static PLACEHOLDER: LazyLock<Regex> = LazyLock::new(|| Regex::new(r"reactorcide-secret-[A-Za-z0-9_.-]+").unwrap());

const LENIENT: GeneralPurposeConfig = GeneralPurposeConfig::new().with_decode_padding_mode(DecodePaddingMode::Indifferent).with_decode_allow_trailing_bits(true);
const B64: GeneralPurpose = GeneralPurpose::new(&base64::alphabet::STANDARD, LENIENT);
const B64URL: GeneralPurpose = GeneralPurpose::new(&base64::alphabet::URL_SAFE, LENIENT);

#[derive(Default)]
pub struct Secrets {
    values: HashMap<String, Vec<u8>>,
    /// All forms of all secrets, longest first.
    any: Option<Regex>,
    /// Form -> secret id.
    forms: HashMap<Vec<u8>, String>,
}

impl Secrets {
    /// Reads one secret from each file in `dir`. The file name is the
    /// secret id. Names that start with "." are skipped (Kubernetes puts
    /// its own links there). A final newline is not part of the value.
    pub fn load_dir(dir: &Path) -> anyhow::Result<Self> {
        let mut items = Vec::new();
        for e in std::fs::read_dir(dir)? {
            let e = e?;
            let name = e.file_name().to_string_lossy().to_string();
            if name.starts_with('.') || !std::fs::metadata(e.path())?.is_file() {
                continue;
            }
            let mut v = std::fs::read(e.path())?;
            while v.last().is_some_and(|b| *b == b'\n' || *b == b'\r') {
                v.pop();
            }
            items.push((name, v));
        }
        Ok(Self::new(items))
    }

    pub fn new(items: Vec<(String, Vec<u8>)>) -> Self {
        let mut s = Secrets::default();
        for (id, v) in items {
            if v.len() < MIN_SECRET {
                tracing::warn!(secret = %id, "secret is too short to scan; it is not loaded");
                continue;
            }
            for f in forms(&v) {
                s.forms.entry(f).or_insert_with(|| id.clone());
            }
            s.values.insert(id, v);
        }
        let mut all: Vec<&Vec<u8>> = s.forms.keys().collect();
        all.sort_by_key(|f| std::cmp::Reverse(f.len()));
        if !all.is_empty() {
            let alt: Vec<String> = all.iter().map(|f| regex::escape(&String::from_utf8_lossy(f))).collect();
            s.any = Some(Regex::new(&alt.join("|")).expect("secret pattern"));
        }
        s
    }

    pub fn get(&self, id: &str) -> Option<&[u8]> {
        self.values.get(id).map(|v| v.as_slice())
    }

    pub fn len(&self) -> usize {
        self.values.len()
    }

    pub fn is_empty(&self) -> bool {
        self.values.is_empty()
    }

    /// Replaces each form of each secret. Returns the new data and the ids.
    fn replace(&self, data: &[u8], ids: &mut Vec<String>) -> Vec<u8> {
        let Some(re) = &self.any else { return data.to_vec() };
        if !re.is_match(data) {
            return data.to_vec();
        }
        re.replace_all(data, |c: &regex::bytes::Captures| {
            let id = self.forms.get(&c[0]).cloned().unwrap_or_default();
            let mark = format!("[redacted:{id}]").into_bytes();
            ids.push(id);
            mark
        })
        .into_owned()
    }
}

/// The forms of a secret that the scan finds directly.
fn forms(v: &[u8]) -> Vec<Vec<u8>> {
    let mut f = vec![
        v.to_vec(),
        STANDARD.encode(v).into_bytes(),
        STANDARD_NO_PAD.encode(v).into_bytes(),
        URL_SAFE.encode(v).into_bytes(),
        URL_SAFE_NO_PAD.encode(v).into_bytes(),
        hex::encode(v).into_bytes(),
        hex::encode_upper(v).into_bytes(),
        percent(v).into_bytes(),
    ];
    f.sort();
    f.dedup();
    f
}

fn percent(v: &[u8]) -> String {
    v.iter()
        .map(|b| match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'.' | b'_' | b'~' => (*b as char).to_string(),
            _ => format!("%{b:02X}"),
        })
        .collect()
}

#[derive(Debug, Default, PartialEq)]
pub struct Scanned {
    pub data: Vec<u8>,
    /// Ids of the secrets that the scan removed.
    pub redacted: Vec<String>,
    /// Ids of secrets found in a form that the scan cannot remove, such
    /// as inside percent-encoded text. The flow must stop.
    pub hidden: Vec<String>,
    /// The deepest encoding found: 0 when the data has no encoded run.
    pub depth: u32,
}

/// Scans `data`. It decodes at most `limit + 1` levels, so the caller can
/// see that the depth is more than `limit`.
pub fn scan(data: &[u8], secrets: &Secrets, limit: u32) -> Scanned {
    let mut s = Scanned::default();
    s.data = level(data, secrets, 1, limit + 1, &mut s);
    if data.contains(&b'%') {
        let plain = unpercent(&s.data);
        let mut ids = Vec::new();
        let _ = secrets.replace(&plain, &mut ids);
        s.hidden.extend(ids);
    }
    s.redacted.sort();
    s.redacted.dedup();
    s.hidden.sort();
    s.hidden.dedup();
    s
}

fn level(data: &[u8], secrets: &Secrets, n: u32, max: u32, s: &mut Scanned) -> Vec<u8> {
    let d = secrets.replace(data, &mut s.redacted);
    if n > max || !RUN.is_match(&d) {
        return d;
    }
    let mut out = Vec::with_capacity(d.len());
    let mut last = 0;
    for m in RUN.find_iter(&d) {
        let Some(text) = decode_run(m.as_bytes()) else { continue };
        s.depth = s.depth.max(n);
        let mut inner = Scanned::default();
        let _ = level(&text, secrets, n + 1, max, &mut inner);
        s.depth = s.depth.max(inner.depth);
        if !inner.redacted.is_empty() {
            out.extend_from_slice(&d[last..m.start()]);
            inner.redacted.sort();
            inner.redacted.dedup();
            out.extend_from_slice(format!("[redacted:{}]", inner.redacted.join(",")).as_bytes());
            last = m.end();
            s.redacted.extend(inner.redacted);
        }
    }
    out.extend_from_slice(&d[last..]);
    out
}

/// Decodes a run as hex or base64. Returns the text only when it is text
/// (or gzip of text): binary data is not a layer of encoding to scan.
fn decode_run(run: &[u8]) -> Option<Vec<u8>> {
    let raw = if run.len().is_multiple_of(2) && run.iter().all(u8::is_ascii_hexdigit) {
        hex::decode(run).ok()?
    } else if run.iter().any(|b| *b == b'-' || *b == b'_') {
        B64URL.decode(run).ok()?
    } else {
        B64.decode(run).ok()?
    };
    let raw = if raw.starts_with(&[0x1f, 0x8b]) { gunzip(&raw, 16 << 20)? } else { raw };
    is_text(&raw).then_some(raw)
}

/// True when most bytes are printable text.
fn is_text(b: &[u8]) -> bool {
    if b.len() < 12 {
        return false;
    }
    let Ok(s) = std::str::from_utf8(b) else { return false };
    let good = s.chars().filter(|c| !c.is_control() || matches!(c, '\n' | '\r' | '\t')).count();
    good * 100 >= s.chars().count() * 95
}

pub fn gunzip(b: &[u8], max: usize) -> Option<Vec<u8>> {
    let mut out = Vec::new();
    flate2::read::GzDecoder::new(b).take(max as u64 + 1).read_to_end(&mut out).ok()?;
    (out.len() <= max).then_some(out)
}

fn unpercent(b: &[u8]) -> Vec<u8> {
    let mut out = Vec::with_capacity(b.len());
    let mut i = 0;
    while i < b.len() {
        if b[i] == b'%'
            && i + 2 < b.len()
            && let Ok(v) = u8::from_str_radix(std::str::from_utf8(&b[i + 1..i + 3]).unwrap_or("zz"), 16)
        {
            out.push(v);
            i += 3;
            continue;
        }
        out.push(if b[i] == b'+' { b' ' } else { b[i] });
        i += 1;
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    const SECRET: &str = "s3cr3t-value-0123456789";

    fn secrets() -> Secrets {
        Secrets::new(vec![("gh".into(), SECRET.into()), ("short".into(), b"abc".to_vec())])
    }

    #[test]
    fn short_secrets_are_not_loaded() {
        assert_eq!(secrets().len(), 1);
    }

    #[test]
    fn removes_raw_and_encoded_forms() {
        let s = secrets();
        for form in [SECRET.to_string(), STANDARD.encode(SECRET), hex::encode(SECRET), URL_SAFE_NO_PAD.encode(SECRET)] {
            let r = scan(format!("token={form}&x=1").as_bytes(), &s, 2);
            assert_eq!(r.redacted, vec!["gh"], "{form}");
            assert_eq!(String::from_utf8(r.data).unwrap(), "token=[redacted:gh]&x=1");
        }
    }

    #[test]
    fn removes_a_secret_inside_encoded_text() {
        let s = secrets();
        let basic = STANDARD.encode(format!("user:{SECRET}"));
        let r = scan(format!("Basic {basic}").as_bytes(), &s, 2);
        assert_eq!(r.redacted, vec!["gh"]);
        assert_eq!(String::from_utf8(r.data).unwrap(), "Basic [redacted:gh]");
        assert_eq!(r.depth, 1);
    }

    #[test]
    fn counts_nested_encodings() {
        let s = Secrets::default();
        let text = "a plain message that is long enough to count";
        let one = STANDARD.encode(text);
        let two = STANDARD.encode(&one);
        let three = STANDARD.encode(&two);
        assert_eq!(scan(text.as_bytes(), &s, 2).depth, 0);
        assert_eq!(scan(one.as_bytes(), &s, 2).depth, 1);
        assert_eq!(scan(two.as_bytes(), &s, 2).depth, 2);
        assert_eq!(scan(three.as_bytes(), &s, 2).depth, 3);
        // Hex inside base64 is also a level.
        assert_eq!(scan(STANDARD.encode(hex::encode(text)).as_bytes(), &s, 2).depth, 2);
    }

    #[test]
    fn finds_a_secret_three_levels_down() {
        let s = secrets();
        let three = STANDARD.encode(STANDARD.encode(STANDARD.encode(format!("key {SECRET} end"))));
        let r = scan(three.as_bytes(), &s, 2);
        assert_eq!(r.redacted, vec!["gh"]);
        assert!(r.depth >= 3);
    }

    #[test]
    fn gzip_in_base64_is_decoded() {
        use std::io::Write as _;
        let mut z = flate2::write::GzEncoder::new(Vec::new(), flate2::Compression::default());
        z.write_all(format!("hidden {SECRET} in gzip").as_bytes()).unwrap();
        let b = STANDARD.encode(z.finish().unwrap());
        let r = scan(format!("{{\"data\":\"{b}\"}}").as_bytes(), &secrets(), 2);
        assert_eq!(r.redacted, vec!["gh"]);
        assert_eq!(String::from_utf8(r.data).unwrap(), "{\"data\":\"[redacted:gh]\"}");
    }

    #[test]
    fn binary_and_identifiers_are_not_levels() {
        let s = Secrets::default();
        assert_eq!(scan(b"/repos/catalystcommunity/reactorcide/contents/README.md", &s, 2).depth, 0);
        assert_eq!(scan(STANDARD.encode([0u8, 1, 2, 3, 250, 251, 252, 253, 7, 8, 9, 10, 11, 12, 13, 14, 15]).as_bytes(), &s, 2).depth, 0);
        assert_eq!(scan(b"ThisIsAVeryLongCamelCaseIdentifierName", &s, 2).depth, 0);
    }

    #[test]
    fn percent_encoded_parts_are_hidden() {
        let s = secrets();
        // The whole secret percent-encoded is a known form.
        let r = scan(format!("q={}", percent(SECRET.as_bytes())).as_bytes(), &s, 2);
        assert_eq!(r.redacted, vec!["gh"]);
        // Only some characters encoded: found after decoding, not removable.
        let partial = SECRET.replace('-', "%2D").replacen('s', "%73", 1);
        let r = scan(format!("q={partial}").as_bytes(), &s, 2);
        assert!(r.redacted.is_empty());
        assert_eq!(r.hidden, vec!["gh"]);
    }

    #[test]
    fn placeholders() {
        assert!(PLACEHOLDER.is_match(b"Bearer reactorcide-secret-0123abcd"));
        assert!(!PLACEHOLDER.is_match(b"Bearer reactorcide-secret"));
        assert!(!PLACEHOLDER.is_match(b"Bearer magi-secret-x"));
    }
}
