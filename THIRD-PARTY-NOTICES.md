# References and third-party notices

This is a new Go implementation. The Desktop IPC framing, read-only data-source
selection, token accounting and original model-interval timing were studied
from these MIT-licensed projects:

- [dongyue-19/codex-statusbar](https://github.com/dongyue-19/codex-statusbar),
  reference commit `17f0ddd25512b9a823b56a37da325d36ca53971f`.
  Copyright (c) 2026 CodexStatusbar contributors.
- [soleillevant0125/codex-token-overlay](https://github.com/soleillevant0125/codex-token-overlay),
  Copyright (c) 2026 soleillevant0125. Its active-route broadcast handling was
  also consulted. License texts are included in `LICENSES/`.

DSH-style cumulative decode statistics were studied from the installed DeepSeek
Harness 0.2.1-rc.2 session-stats and ui-chat packages and the
[official documentation](https://github.com/deepseek-ai/deepseek-harness/blob/master/packages/session/session-stats/README.md).
This Go aggregation is independently implemented. The former character-based
dsh-status-bar reference estimator is no longer used.

The Go dependency `golang.org/x/sys` is distributed under the Go project's
BSD-style license, included in `LICENSES/golang-x-sys-BSD.txt`.

`github.com/klauspost/compress` v1.20.3 supplies the pure Go Zstandard decoder.
Its license is included in `LICENSES/klauspost-compress-LICENSE.txt`.
No separate decoder executable or native compression DLL is required.

Microsoft Windows system APIs and system libraries are dynamically loaded from
Windows, not redistributed. Codex is a trademark of OpenAI. This companion is
independent of OpenAI. Desktop's IPC and persisted-record formats are internal
implementation details and require compatibility checks after client updates.
