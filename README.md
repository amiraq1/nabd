# NabdCoder

وكيل برمجة متخصص في مشروع Nabd، محسّن لبيئة Termux (Android/ARM64).

## المميزات

- **Workspace Jail**: كل عمليات الملفات تمر عبر `os.Root` على جذر المشروع، فلا تخرج المسارات أو symlinks عن مجلد العمل.
- **Approval Gate**: تأكيد يدوي يعرض المسار وdiff وSHA-256؛ لا يُقبل التأكيد إذا كانت المعاينة مقتطعة أو ناقصة.
- **edit_file**: استبدال نص دقيق + فحص syntax لملفات Go، وكتابة ذرّية (temp + fsync + rename) تحافظ على صلاحيات الملف.
- **go_verify**: `go test -vet=all` مع `GOMEMLIMIT`, `GOGC`, `-p 2`، مهلة ومشرف فرعي لكل أمر ينظف شجرته على Linux/Android، ونتيجة منظمة بدل السجلات الخام. إذا لم تتوفر آليات الإشراف المطلوبة، لا يبدأ الأمر.
- **Shadow Storage**: نسخة قبل كل تعديل في `.nabd/shadow`، واستعادة عبر `undo [path]`.
- **Audit log**: سجل append-only في `.nabd/audit.log` بحداثات SHA-256 فقط، بلا محتوى ملفات.
- **LLM client**: واجهة `llm.Client` قابلة للاختبار، تنفيذ OpenAI-compatible، retries، ومهلة كلية وإلغاء فوري عند Ctrl+C.

## البنية

- `cmd/agent`: واجهة TUI وحلقة الوكيل.
- `cmd/mcp-server`: خادم MCP للقراءة فقط (لا approver).
- `internal/tools`: سجل الأدوات، الـjail، الموافقات، والتدقيق.
- `internal/shadow`: نسخ ما قبل التعديل والاحتفاظ.
- `internal/audit`: كتابة JSONL للتدقيق.
- `internal/llm`: الواجهة وعميل OpenAI-compatible.

## البناء في Termux

```bash
pkg install golang git
go version  # يجب أن يكون 1.25.5 أو أحدث
git clone https://github.com/amiraq1/nabd.git
cd nabd
go mod download
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $PREFIX/bin/nabdcoder ./cmd/agent
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $PREFIX/bin/nabd-mcp ./cmd/mcp-server
nabdcoder
```

## الإعداد

كل الإعداد من البيئة، بلا flags على سطر الأوامر:

- `NABD_BASE_URL` — افتراضي `http://127.0.0.1:8080/v1`
- `NABD_MODEL` — افتراضي `local`
- `NABD_API_KEY` — افتراضي `sk-no-key`

## الأدوات وأوامر TUI

| الأداة    | الوصف                              | تأكيد؟ |
|-----------|------------------------------------|--------|
| read_file | قراءة ملف على دفعات عبر `offset` و`limit` (حتى 3584 بايت) | لا     |
| edit_file | استبدال نص دقيق + فحص syntax       | نعم    |
| go_verify | go test + vet مع قيود ذاكرة        | نعم    |
| list_dir  | عرض محتويات مجلد                   | لا     |
| mcp_call  | استدعاء أداة على خادم MCP خارجي    | نعم    |

أوامر TUI: `clear`, `undo [path]`, `exit`/`quit`, و`Ctrl+C`.

## الأمان

- **Jail**: `os.Root` + تحقق من المسار، ورفض symlinks في `edit_file`/`undo`، ورفض المسارات الحساسة: `.env*` (عدا القوالب)، `.git/`، `.nabd/`، `id_*` (عدا `.pub`)، وملفات المفاتيح.
- **Gate**: preview + diff فعلي + hashes، وtoken أحادي الاستخدام مربوط بالأداة و`args_sha256` ومسار الملف وبصمة SHA-256 لـ`edit_file`. يحتفظ الرمز بdescriptor الملف الموافق عليه حتى استخدامه أو انتهاءه؛ ويتحقق التنفيذ من inode والمحتوى بعد تجهيز المؤقت وقبل النسخة الاحتياطية وقبل rename. صلاحية الرمز 5 دقائق.
- **Audit**: أحداث `requested`/`approved`/`denied`/`applied`/`failed` مع hashes؛ الكتابة إلزامية للأدوات الكاتبة/المنفذة، وbest-effort للقراءة.
- **go_verify**: بيئة allowlist بلا مفاتيح، `GOFLAGS=-mod=readonly`، `GOPROXY=off`، `GOTOOLCHAIN=local`، وأنماط حزم workspace-relative فقط.
- **mcp-server**: يعلن أدوات القراءة فقط؛ لا يعلن `edit_file` أو`go_verify` أو`mcp_call`. يعلن مخطط JSON لكل أداة لعملاء MCP.
- **mcp_call**: قيم النص في معاملات MCP تُحجب احتياطياً؛ لذلك تُمنع الموافقة على أي استدعاء يتضمن قيماً نصية إلى أن تتوفر معاينة واعية بمخطط الأداة. هذا يمنع عرض محتوى قد يكون سراً، لكنه يحدّ من الأدوات التي يمكن استدعاؤها.
- **إشراف العمليات**: على Linux/Android، مشرف فرعي منفصل لكل `go_verify` و`mcp_call` يتتبع أحفاده حتى بعد تغيّر الجلسة أو مجموعة العمليات، ويستخدم pidfd لتفادي استهداف PID أُعيد استخدامه. يلزم `/proc` و`PR_SET_CHILD_SUBREAPER` وpidfd؛ إذا غاب أي منها، يُرفض تشغيل الأمر. الأنظمة الأخرى fail-closed، لا تستخدم قتل مجموعة واسعاً كبديل.

راجع [docs/THREAT_MODEL.md](docs/THREAT_MODEL.md) لما يضمنه النظام وما لا يضمنه.

## الاختبارات و CI

```bash
gofmt -l .          # يجب أن يكون فارغاً
go vet ./...
go test ./... -count=1
staticcheck ./...
```

`.github/workflows/ci.yml` يشغّل gofmt وvet و`go mod verify` وtest وstaticcheck، ويبني `linux/amd64` و`linux/arm64` بـ`CGO_ENABLED=0` على كل push إلى `main`/`audit/**` وكل PR.

## القيود والمخاطر المعروفة

- حدود الحجم: قراءة حتى 3584 بايت لكل دفعة عبر `read_file` (استخدم `offset` للوصول إلى الأجزاء التالية)، قائمة 1000 مدخل، تعديل 2MiB، ومخرجات `go test` محفوظة بحد أقصى 1MiB قبل تلخيصها.
- Shadow: آخر 20 نسخة لكل مسار أو 7 أيام؛ الكتابة الذرّية عبر rename تفصل hard links.
- تعديل الملفات يعيد التحقق من inode والبصمة قبل النسخة الاحتياطية وقبل rename، ويُسلسل التعديل وundo داخل العملية. لا توجد مقارنة-واستبدال ذرية محمولة؛ يبقى احتمال ضيق لتعديل من كاتب خارجي غير متعاون بين آخر تحقق وrename.
- Audit log append-only لكنه ليس tamper-evident، وينمو بلا تدوير حالياً.
- `mcp_call` و`go_verify` يشغّلان كوداً بصلاحيات المستخدم بلا sandbox؛ بيئتهما محدودة ولا تتضمن `NABD_API_KEY`، لكن ذلك لا يمنع الوصول إلى ملفات المستخدم أو الشبكة أثناء التشغيل.
- `read_file` يرسل محتوى الملفات إلى endpoint الـLLM؛ سياسة الأسرار مبنية على الأسماء لا على المحتوى.
- `go_verify` و`mcp_call` يحتاجان موافقة، لكن الموافقة ليست عزلاً.
- Windows والأنظمة غير Linux: `go_verify` و`mcp_call` معطّلان fail-closed حتى يتوفر احتواء موثوق؛ لا يوجد fallback إلى قتل process group أو PPID أوسع.
- لا يوجد `LICENSE` في المستودع بعد.

## التحقق على Termux

DNS/TLS لثنائي `CGO_ENABLED=0` يعتمد على إعداد الجهاز. شغّل:

```bash
cd "$(mktemp -d)" && cat > probe.go <<'EOF'
package main
import ("crypto/tls";"fmt";"net";"net/http";"os";"time")
func main() {
  fmt.Println("PREFIX:", os.Getenv("PREFIX"))
  ips, err := net.LookupHost("example.com"); fmt.Println("DNS:", ips, err)
  _, err = tls.Dial("tcp", "example.com:443", &tls.Config{ServerName: "example.com"}); fmt.Println("TLS:", err)
  _, err = (&http.Client{Timeout: 10 * time.Second}).Get("https://example.com"); fmt.Println("HTTPS:", err)
}
EOF
CGO_ENABLED=0 go run probe.go; echo "GOOS=$(go env GOOS) GOARCH=$(go env GOARCH)"
```

المتوقع عند سلامة الشبكة:

```text
PREFIX: /data/data/com.termux/files/usr
GOOS=android GOARCH=arm64
DNS: [93.184.215.14 ...] <nil>
TLS: <nil>
HTTPS: <nil>
```

ظهور `lookup ... 127.0.0.1:53` يعني مشكلة DNS في resolver النقي؛ وظهور `x509: certificate signed by unknown authority` يعني مشكلة CA (جرّب `SSL_CERT_FILE=$PREFIX/etc/tls/cert.pem`).

صُمم للعمل داخل مجلد مشروع Nabd.
