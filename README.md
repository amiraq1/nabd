# NabdCoder

وكيل برمجة متخصص في مشروع Nabd، محسّن لبيئة Termux (Android/ARM64).

## المميزات

- **Workspace Jail**: كل عمليات الملفات تمر عبر `os.Root` على جذر المشروع، فلا تخرج المسارات أو symlinks عن مجلد العمل.
- **Approval Gate**: تأكيد يدوي يعرض المسار وdiff وSHA-256 قبل أي تعديل أو تنفيذ.
- **edit_file**: استبدال نص دقيق + فحص syntax لملفات Go، وكتابة ذرّية (temp + fsync + rename) تحافظ على صلاحيات الملف.
- **go_verify**: `go test -vet=all` مع `GOMEMLIMIT`, `GOGC`, `-p 2`، مهلة تقتل شجرة العمليات، ونتيجة منظمة بدل السجلات الخام.
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
| read_file | قراءة ملف                          | لا     |
| edit_file | استبدال نص دقيق + فحص syntax       | نعم    |
| go_verify | go test + vet مع قيود ذاكرة        | نعم    |
| list_dir  | عرض محتويات مجلد                   | لا     |
| mcp_call  | استدعاء أداة على خادم MCP خارجي    | نعم    |

أوامر TUI: `clear`, `undo [path]`, `exit`/`quit`, و`Ctrl+C`.

عند الخروج تُلغى طلبات النموذج والأدوات، ويُنتظر انتهاء الأوامر النشطة حتى 5 ثوانٍ قبل إغلاق سجل الأدوات (`Registry`). في Unix يوقف إلغاء `go_verify` مجموعة عملياته؛ لا يشمل ذلك عملية انفصلت إلى مجموعة أخرى، ولا يعدّ عزلاً أمنياً.

## الأمان

- **Jail**: `os.Root` + تحقق من المسار، ورفض symlinks في `edit_file`/`undo`، ورفض المسارات الحساسة: `.env*` (عدا القوالب)، `.git/`، `.nabd/`، `id_*` (عدا `.pub`)، وملفات المفاتيح.
- **Gate**: preview + diff + hashes، وtoken أحادي الاستخدام مربوط بالأداة و`args_sha256`، صلاحيته 5 دقائق. لا يمكن استدعاء `RunApproved` بدونه.
- **Audit**: أحداث `requested`/`approved`/`denied`/`applied`/`failed` مع hashes؛ الكتابة إلزامية للأدوات الكاتبة/المنفذة، وbest-effort للقراءة.
- **go_verify**: بيئة allowlist بلا مفاتيح، `GOFLAGS=-mod=readonly`، `GOPROXY=off`، `GOTOOLCHAIN=local`، وأنماط حزم workspace-relative فقط.
- **mcp-server**: للقراءة فقط؛ يرفض الأدوات غير read-only لغياب approver، و`mcp_call` معطّل فيه.
- **CLI/TUI**: `process-group kill` على Unix/Termux، و`taskkill /T` كأفضل جهد على Windows.

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

- حدود الحجم: قراءة 512KiB، تعديل ونسخته الاحتياطية 2MiB، وسجل `go_verify` يحتفظ بـ64KiB من بدايته ونهايته. تُعرض حتى 1000 نتيجة من أول 4096 مدخلاً مفحوصاً من المجلد، مع تنبيه عند بلوغ حد المسح؛ الترتيب يخص الجزء المفحوص فقط.
- Shadow: آخر 20 نسخة لكل مسار أو 7 أيام، وحصة إجمالية 128MiB لمحتويات مخزن النسخ. فحص الحصة يحسب الملفات المتروكة، لكنه لا ينسّق مع عمليات Nabd أخرى أو الكتابة الخارجية المتزامنة. الكتابة الذرّية عبر rename تفصل hard links.
- Audit log append-only لكنه ليس tamper-evident، وينمو بلا تدوير حالياً.
- `mcp_call` و`go_verify` يشغّلان كوداً بصلاحيات المستخدم بلا sandbox؛ كود الاختبار يستطيع استخدام الشبكة رغم `GOPROXY=off`.
- `mcp_call` لم يُدمج معه F-24: العملية قد ترث البيئة، ولا يوجد حد للرد عند النقل أو ضمان لتنظيف الأحفاد المنفصلين، والعميل الحالي لا ينفّذ تهيئة MCP قبل الاستدعاء. لا تستخدمه حتى يُعالج ذلك.
- `read_file` يرسل محتوى الملفات إلى endpoint الـLLM؛ سياسة الأسرار مبنية على الأسماء لا على المحتوى.
- `go_verify` و`mcp_call` يحتاجان موافقة، لكن الموافقة ليست عزلاً.
- Windows: قتل الشجرة best-effort، والصلاحيات approximated.
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
