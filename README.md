# NabdCoder

وكيل برمجة متخصص في مشروع Nabd، محسّن لبيئة Termux (Android/ARM64).

## المميزات

- **Workspace Jail**: عمليات ملفات الأدوات تمر عبر `os.Root`؛ هذا لا يعزل العمليات الخارجية ولا يزيل كل سباقات المسارات.
- **Approval Gate**: تأكيد يدوي يعرض المسار وdiff وSHA-256 قبل أي تعديل أو تنفيذ.
- **edit_file**: استبدال نص دقيق + فحص syntax لملفات Go، وكتابة ذرّية (temp + fsync + rename) تحافظ على صلاحيات الملف.
- **go_verify وmcp_call**: معطّلتان في الوكيل؛ لا يوجد عزل ملفات وشبكة كافٍ لتشغيل كود غير موثوق.
- **Shadow Storage**: نسخة قبل كل تعديل في `.nabd/shadow`، واستعادة عبر `undo [path]`.
- **Audit log**: سجل append-only في `.nabd/audit.log` يحوي بصمات وبعض البيانات النصية مثل المسارات؛ ليس محصّناً من العبث ولا يضمن تسجيل نتيجة العملية بعد وقوعها.
- **LLM client**: واجهة `llm.Client` قابلة للاختبار، تنفيذ OpenAI-compatible، retries، ومهلة كلية وإلغاء فوري عند Ctrl+C.

تُقبل عناوين مزوّد النموذج عبر HTTPS، أو HTTP على localhost وعناوين loopback فقط. لا يتبع العميل إعادة التوجيه، لتجنب إرسال بيانات الاعتماد إلى وجهة أخرى.

## البنية

- `cmd/agent`: واجهة TUI وحلقة الوكيل.
- `cmd/mcp-server`: خادم MCP للقراءة فقط (لا approver).
- `cmd/mcp-guard`: حارس seccomp المطلوب لتشغيل MCP الخارجي.
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
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $PREFIX/bin/nabd-mcp-guard ./cmd/mcp-guard
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
| read_file | قراءة صفحة من ملف باستخدام `offset` و`limit` بالبايت | موافقة قبل إرسال النتيجة إلى النموذج |
| edit_file | استبدال نص دقيق + فحص syntax       | نعم    |
| go_verify | تنفيذ الاختبارات | معطّل في الوكيل |
| list_dir  | عرض محتويات مجلد                   | لا     |
| mcp_call  | استدعاء أداة على خادم MCP خارجي    | معطّل في الوكيل |

أوامر TUI: `clear`, `undo [path]`, `exit`/`quit`, و`Ctrl+C`.

عند الخروج تُلغى طلبات النموذج والأدوات، ويُنتظر انتهاء الأوامر النشطة حتى 5 ثوانٍ قبل إغلاق سجل الأدوات (`Registry`). في Unix يوقف إلغاء `go_verify` مجموعة عملياته؛ لا يشمل ذلك عملية انفصلت إلى مجموعة أخرى، ولا يعدّ عزلاً أمنياً.

## الأمان

- **Jail**: `os.Root` + تحقق من المسار، ورفض symlinks في `edit_file`/`undo`، ورفض المسارات الحساسة: `.env*` في كل مكوّن (عدا ملفات `.env.example` و`.env.sample` و`.env.template` و`.env.dist` النهائية؛ لا يُفحص محتواها)، `.git/`، `.nabd/`، `id_*` (عدا `.pub`)، وملفات المفاتيح.
- على Linux/Android تُفتح ملفات القراءة وكل مكوّنات مسارها دون اتباع الروابط الرمزية؛ هذا لا يغلق سباقات المسار في كل عمليات الكتابة، ولا ينطبق الضمان نفسه على المنصات الأخرى.
- **Gate**: preview + diff + hashes، وtoken أحادي الاستخدام مربوط بالأداة و`args_sha256`، صلاحيته 5 دقائق. لا يمكن استدعاء `RunApproved` بدونه.
- **Audit**: أحداث `requested`/`approved`/`denied`/`applied`/`failed` مع hashes؛ الكتابة إلزامية للأدوات الكاتبة/المنفذة، وbest-effort للقراءة.
- **go_verify وmcp_call**: محذوفتان من قائمة أدوات الوكيل ولا تُنفّذان عبره. الكود الداخلي المتبقي لا يشكّل sandbox، ولا ينبغي استخدامه مباشرة لتشغيل مشاريع أو خوادم غير موثوقة.
- **mcp-server**: لا يعرض أدوات التعديل والتنفيذ؛ قد يكتب سجل التدقيق عند القراءة.
- **CLI/TUI**: `process-group kill` على Unix/Termux، و`taskkill /T` كأفضل جهد على Windows.

راجع [docs/THREAT_MODEL.md](docs/THREAT_MODEL.md) لما يضمنه النظام وما لا يضمنه.

## الاختبارات و CI

```bash
gofmt -l .          # يجب أن يكون فارغاً
go vet ./...
go test ./... -count=1
staticcheck ./...
```

`.github/workflows/ci.yml` يشغّل gofmt وvet و`go mod verify` والاختبارات و`go test -race -short` وstaticcheck بإصدار محدد وgovulncheck، ويبني `linux/amd64` و`linux/arm64` و`android/arm64` بـ`CGO_ENABLED=0` على كل push إلى `main`/`audit/**` وكل PR. البناء المتقاطع لا يغني عن تشغيل الاختبارات على Termux فعلياً.

## القيود والمخاطر المعروفة

- حد وسائط الموافقة ومعاينتها 4000 بايت؛ قد يُرفض تعديل صغير إذا اضطر إلى تضمين سطر طويل جداً، حتى لو لم يمكن تقسيمه.
- تعطيل `mcp_call` و`go_verify` خاص بالوكيل؛ لا تُعدّ الأدوات الداخلية المتبقية آمنة لتشغيل تعليمات غير موثوقة.
- الكود الداخلي لـ`mcp_call` لا يربط هوية الملف التنفيذي أو الحارس بالموافقة؛ يجب عدم إعادة تفعيله قبل معالجة ذلك وتوفير العزل.
- استثناءات `.env` للملفات النهائية فقط: مجلد باسم `.env.example` محجوب، حتى لو كان اسمه شبيهاً باسم قالب مسموح.
- على Windows لم تُختبر وقت التشغيل معالجة النقاط والمسافات النهائية في مكوّنات المسار مقابل قواعد الأسماء الحساسة؛ لا تعتمد على المنع الاسمي وحده هناك.
- CI يثبت إصدار staticcheck ويفحص الثغرات باستخدام govulncheck؛ يجب إبقاء أداة Go والتبعيات محدّثتين.

- حدود الحجم: قراءة حتى 3584 بايت لكل صفحة (قد تتمدّد حتى ثلاثة بايتات لإكمال محرف UTF-8)، تعديل ونسخته الاحتياطية 2MiB، وسجل `go_verify` يحتفظ بـ64KiB من بدايته ونهايته. تُعرض حتى 1000 نتيجة من أول 4096 مدخلاً مفحوصاً من المجلد، مع تنبيه عند بلوغ حد المسح؛ الترتيب يخص الجزء المفحوص فقط.
- Shadow: تُطبق سياسة آخر 20 نسخة لكل مسار أو 7 أيام عند إنشاء نسخة جديدة فقط، وليست موعد حذف مضموناً؛ وفحص حصة 128MiB قبل إضافة نسخة احتياطية. يشمل الفحص الملفات المتروكة، لكنه لا ينسّق مع عمليات Nabd الأخرى أو الكتابة الخارجية المتزامنة، ولا يمثل ضماناً صارماً لاستخدام القرص في كل اللحظات. الكتابة الذرّية عبر rename تفصل hard links.
- Audit log append-only لكنه ليس tamper-evident، وينمو بلا تدوير حالياً.
- إن استُخدم الكود الداخلي مباشرة، يشغّل `mcp_call` و`go_verify` كوداً بصلاحيات المستخدم بلا sandbox؛ كود الاختبار يستطيع استخدام الشبكة رغم `GOPROXY=off`.
- الحارس ليس sandbox: لا يقيّد وصول MCP للشبكة أو الملفات أو تشغيل برامج أخرى، ولا يمنع كل طرق إنشاء عمليات منفصلة أو الهروب عبر أنظمة خارجية. لم تُختبر هجمات namespaces، ولا تعتمد عليه لاحتواء برنامج خبيث.
- في التنفيذ الداخلي المعطّل تُقتل مجموعة MCP قبل إعادة حصاد عملية الحارس على Linux/Android؛ تنظيف عمليات انفصلت بطرق لم يغلقها الفلتر غير مضمون. لا دعم لـ`mcp_call` خارج المنصات المذكورة. لم يُتحقق من هذا التغيير على Termux فعلياً.
- يعرض الوكيل ناتج `read_file` والوجهة للمراجعة، ولا يرسلهما إلى النموذج إلا بعد الموافقة. يجب التمرير إلى النهاية قبل الموافقة. تُعرض البيانات بصيغة ASCII مقتبسة وتُرفض المخرجات غير الآمنة للعرض. سياسة الأسرار مبنية على الأسماء لا المحتوى؛ ومخرجات الأدوات الأخرى والرسائل التي يكتبها المستخدم قد تتضمن بيانات حساسة.
- الموافقة ليست عزلاً؛ أداتا التنفيذ معطلتان في الوكيل إلى حين وجود sandbox فعلي.
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

`read_file` تقرأ صفحة لا تتجاوز 3584 بايت افتراضياً، وتعرض offset للمتابعة عند الاقتطاع. قد يمتد الحد حتى 3 بايت لإكمال أول حرف UTF-8، وتُستبدل البايتات غير الصالحة بمحرف بديل. للاستجابة المنظمة استخدم `format: "json"` للحصول على `content` و`truncated` و`next_offset` عند الحاجة؛ الوضع النصي يبقى الافتراضي للتوافق وقد يحتوي محتواه ما يشبه علامة المتابعة.
