# MailSorter

Cross-platform tool that files mail into folders. This repository is at phase 0: the scaffold builds, `mailsorter version` works, and every other command reports that it is not implemented yet.

The spec, dependency pins, and provider notes are in [docs/SPEC.md](docs/SPEC.md). Safety rules are in [AGENTS.md](AGENTS.md).

```bash
make test
make build
./dist/mailsorter-linux-amd64 version
```

Setup guides for Gmail, Microsoft 365, and automation are written in a later phase.

Кратко: это каркас программы, которая раскладывает почту по папкам. Сейчас работает только команда `version`. Полная инструкция на русском появится вместе с интерфейсом.
