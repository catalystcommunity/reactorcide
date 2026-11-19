//! A plugin for the proxy tests. The header "x-test-plugin" lists
//! commands, separated by ";": "block:R", "review:R", "body:TEXT",
//! "header:NAME=VALUE", "secret:PLACEHOLDER:ID:PLACE", "ask:OP", "loop",
//! "memory", "panic".

wit_bindgen::generate!({ path: "../../wit/egress.wit", world: "filter" });

use reactorcide::egress::host;
use reactorcide::egress::types::{Header, SecretUse};

struct Test;

impl Guest for Test {
    fn check(f: Flow) -> Vec<Action> {
        let Some(h) = f.headers.iter().find(|h| h.name.eq_ignore_ascii_case("x-test-plugin")) else { return vec![Action::Pass] };
        let mut out = Vec::new();
        for c in h.value.split(';').map(str::trim) {
            let (cmd, arg) = c.split_once(':').unwrap_or((c, ""));
            match cmd {
                "block" => out.push(Action::Block(arg.into())),
                "review" => out.push(Action::Review(arg.into())),
                "body" => out.push(Action::SetBody(arg.as_bytes().to_vec())),
                "header" => {
                    let (n, v) = arg.split_once('=').unwrap_or((arg, ""));
                    out.push(Action::SetHeader(Header { name: n.into(), value: v.into() }));
                }
                "secret" => {
                    let p: Vec<&str> = arg.splitn(3, ':').collect();
                    out.push(Action::UseSecret(SecretUse { placeholder: p[0].into(), secret: p[1].into(), place: p[2].into() }));
                }
                "ask" => {
                    let mut req = Vec::new();
                    let m = ciborium::Value::Map(vec![("question".into(), "is this fine?".into()), ("plugin".into(), "forged".into())]);
                    ciborium::into_writer(&m, &mut req).unwrap();
                    match host::ask(arg, &req) {
                        Ok(b) => out.push(Action::SetBody(b)),
                        Err(e) => out.push(Action::Block(format!("ask error: {e}"))),
                    }
                }
                "loop" => {
                    let mut x: u64 = 1;
                    loop {
                        x = x.wrapping_mul(6364136223846793005).wrapping_add(1);
                        if x == 0 {
                            break;
                        }
                    }
                    out.push(Action::Block(format!("{x}")));
                }
                "memory" => {
                    let v = vec![1u8; 512 << 20];
                    out.push(Action::Block(format!("{}", v.iter().map(|b| *b as u64).sum::<u64>())));
                }
                "panic" => panic!("test panic"),
                _ => {}
            }
        }
        out
    }
}

export!(Test);
