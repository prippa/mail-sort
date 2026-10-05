# MailSorter

Cross-platform tool that files mail into folders. It can test an IMAP connection, classify one message, file a mailbox after a confirmed dry run, and open a local web page to set up an account.

The spec, dependency pins, and provider notes are in [docs/SPEC.md](docs/SPEC.md). Safety rules are in [AGENTS.md](AGENTS.md).

```bash
make test
make build
./dist/mailsorter-linux-amd64 version
./dist/mailsorter-linux-amd64 ui
./dist/mailsorter-linux-amd64 test-conn --profile Work
./dist/mailsorter-linux-amd64 dev-clean --profile Work --folder INBOX --limit 5
./dist/mailsorter-linux-amd64 categories export > categories.yaml
./dist/mailsorter-linux-amd64 classify --stdin < message.json
./dist/mailsorter-linux-amd64 run --profile Work
./dist/mailsorter-linux-amd64 run --profile Work --confirm
./dist/mailsorter-linux-amd64 run --profile Work --apply
./dist/mailsorter-linux-amd64 watch --profile Work
./dist/mailsorter-linux-amd64 undo --profile Work
```

`test-conn` and `dev-clean` read `password_env` from the environment for a password account. Google and Microsoft sign in from the local page; the refresh token stays on this computer. `classify` reads `TYPESAFE_API_KEY` for Jev, or the variable named by `key_env`, and then the key saved on this computer. With no `categories.yaml`, classify uses the starter list and does not call a provider until `classifiers` is set.

`watch` files mail that arrives after a dry run has been confirmed and applied. It waits with IMAP IDLE and renews that wait before 29 minutes. Without IDLE it checks every 5 minutes (`--poll 5m`). Ctrl+C or SIGTERM stops it. A dropped connection is opened again, with a pause that grows up to 5 minutes. At most 200 moves are filed, then watch waits one poll interval before the rest. Stop `watch` before starting another dry run.

An unsigned `mailsorter-windows-amd64.exe` can make Windows show SmartScreen.

### systemd user service

`~/.config/systemd/user/mailsorter.service`:

```ini
[Unit]
Description=MailSorter watch
After=network-online.target

[Service]
ExecStart=/usr/local/bin/mailsorter watch --profile Work
EnvironmentFile=%h/.config/mailsorter/watch.env
Restart=on-failure
RestartSec=30

[Install]
WantedBy=default.target
```

`watch.env` is mode `0600`. It can hold `MAIL_SORTER_PASSWORD_…` or `MAIL_SORTER_MASTER_PASSWORD`. It is separate from `config.yaml`. A refresh token stays in the keyring; a user service can use the session bus. Then:

```bash
systemctl --user daemon-reload
systemctl --user enable --now mailsorter.service
```

### Windows scheduled task

Run at logon so the credential store is available. Keep the password in the user environment, not in the task command.

```bat
schtasks /Create /SC ONLOGON /TN MailSorter /TR "C:\path\mailsorter-windows-amd64.exe watch --profile Work"
```

Кратко: программа подключается к почте по IMAP, классифицирует письма и после подтверждения раскладывает их по папкам. Первый запуск профиля только показывает план. Команда `watch` после этого ждёт новую почту и раскладывает её. Пароль и ключ API задаются переменными окружения или хранятся на этом компьютере, а не в файле настроек. Google и Microsoft входят со страницы на этом компьютере. Команда без аргументов открывает эту страницу.
