# imchat

单文件即时通信系统。一个 Go 二进制内嵌前端与 SQLite，`docs/DEPLOY.md` 是部署入口。

## 注释纪律

改动行为必须同步改讲解；新增 func / type 必须有上方契约注释。检查：

```bash
make notes-check
```

理由与边界见 `docs/adr/0006-narration-inside-source.md`——静态工具只能发现结构缺失，检不出语义层面的撒谎，那部分只能靠这条纪律与 review 兜底。

## Agent skills

### Issue tracker

Issues live in this repo's GitHub Issues, driven by the `gh` CLI. See `docs/agents/issue-tracker.md`.

### Triage labels

Five canonical roles mapped to their default label strings. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: one `GLOSSARY.md` and `docs/adr/` at the repo root. See `docs/agents/domain.md`.