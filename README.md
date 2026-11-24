# NabdCoder

وكيل برمجة متخصص في مشروع Nabd، محسّن لبيئة Termux (Android/ARM64).

## المميزات

- **Workspace Jail**: عمليات ملفات الأدوات تمر عبر `os.Root`؛ هذا لا يعزل العمليات الخارجية ولا يزيل كل سباقات المسارات.
- **Approval Gate**: تأكيد يدوي يعرض المسار وdiff وSHA-256 قبل أي تعديل أو تنفيذ.
- **edit_file**: استبدال نص دقيق + فحص syntax لملفات Go، وكتابة ذرّية (temp + fsync + rename) تحافظ على صلاحيات الملف.
- **go_verify وmcp_call**: معطّلتان افتراضياً ولا تُفعّلان إلا بعد نجاح إثبات عزل فعلي (Linux + bubblewrap). لا يُعتبر `os.Root` أو seccomp أو الموافقة وحدها عزلاً.
- **Shadow Storage**: نسخة قبل كل تعديل في `.nabd/shadow`، واستعادة عبر `undo [path]`؛ تنظيف دوري للنسخ المنتهية أثناء تشغيل العملية.
- **Audit log**: سجل JSONL بسلسلة hash في `.nabd/audit.log`، مع حدث `executing` قبل التنفيذ؛ بصمات فقط ووسائط نصية مُطهّرة. التحقق يحتاج مرساة موثوقة خارج السجل.
- **LLM client**: واجهة `llm.Client` قابلة للاختبار، تنفيذ OpenAI-compatible، retries، ومهلة كلية وإلغاء فوري عند Ctrl+C.

تُقبل عناوين مزوّد النموذج عبر HTTPS، أو HTTP على localhost وعناوين loopback فقط. لا يتبع العميل إعادة التوجيه، لتجنب إرسال بيانات الاعتماد إلى وجهة أخرى.

## البنية

- `cmd/agent`: واجهة TUI وحلقة الوكيل.
- `cmd/mcp-server`: خادم MCP للقراءة فقط (لا approver).
- `cmd/mcp-guard`: حارس seccomp المطلوب لتشغيل MCP الخارجي (ليس sandbox وحده).
- `cmd/nabd-limit` و`cmd/nabd-sandbox-probe`: ضابط الموارد داخل النطاق، ومُثبِت القيود عند بدء التشغيل.
- `internal/sandbox`: واجهة العزل وbackend الخاص بـLinux؛ يفشل مغلقاً على المنصات الأخرى.
- `internal/tools`: سجل الأدوات، الـjail، الموافقات، والتدقيق.
- `internal/shadow`: نسخ ما قبل التعديل والاحتفاظ.
- `internal/audit`: كتابة JSONL للتدقيق.
- `internal/llm`: الواجهة وعميل OpenAI-compatible.

## البناء في Termux

```bash
pkg install golang git
go version  # يجب أن يكون 1.26.11 أو أحدث
git clone https://github.com/amiraq1/nabd.git
cd nabd
go mod download
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $PREFIX/bin/nabdcoder ./cmd/agent
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $PREFIX/bin/nabd-mcp ./cmd/mcp-server
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $PREFIX/bin/nabd-mcp-guard ./cmd/mcp-guard
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $PREFIX/bin/nabd-limit ./cmd/nabd-limit
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $PREFIX/bin/nabd-sandbox-probe ./cmd/nabd-sandbox-probe
nabdcoder
```

## الإعداد

كل الإعداد من البيئة، بلا flags على سطر الأوامر:

- `NABD_BASE_URL` — افتراضي `http://127.0.0.1:8080/v1`
- `NABD_MODEL` — افتراضي `local`
- `NABD_API_KEY` — افتراضي `sk-no-key`
- `NABD_MCP_CONFIG` — مسار ملف JSON لقائمة خوادم MCP المسموح بها (مطلوب لتفعيل `mcp_call`). كل خادم يحدد `executable` و`sha256` و`guard` و`guard_sha256` و`env` من قائمة مسموح بها فقط؛ يُرفض أي خادم غير محدد من المستخدم.
- `NABD_EXEC_SANDBOX=1` — موافقة صريحة على محاولة تفعيل التنفيذ الخارجي. لا يُفعّل شيء حتى ينجح `sandbox.Prove`؛ على Termux/Windows أو بدون bubblewrap تبقى الأداتان معطّلتين.
- `NABD_AUDIT_ANCHOR` — قيمة سرية تُحفظ خارج السجل وتُستخدم كمرساة لسلسلة hash؛ بدونها لا يمكن اكتشاف إعادة حساب السلسلة من قبل مستخدم محلي.
- `NABD_SHADOW_RETENTION_INTERVAL` — فاصل التنظيف الدوري (افتراضي 24h)؛ قاعدة الاحتفاظ 7 أيام/20 نسخة لكل مسار.

## الأدوات وأوامر TUI

| الأداة    | الوصف                              | تأكيد؟ |
|-----------|------------------------------------|--------|
| read_file | قراءة صفحة من ملف باستخدام `offset` و`limit` بالبايت | نعم قبل إرسال النتيجة |
| edit_file | استبدال نص دقيق + فحص syntax       | نعم    |
| go_verify | تنفيذ الاختبارات داخل نطاق العزل | معطّل افتراضياً؛ يُقدَّم فقط بعد إثبات العزل |
| list_dir  | عرض محتويات مجلد                   | لا قبل التنفيذ، نعم قبل إرسال النتيجة |
| mcp_call  | استدعاء خادم MCP مُعدّ مسبقاً من المستخدم | معطّل افتراضياً؛ يُقدَّم فقط بعد إثبات العزل |

أوامر TUI: `clear`, `undo [path]`, `exit`/`quit`, و`Ctrl+C`.

عند الخروج تُلغى طلبات النموذج والأدوات، ويُنتظر انتهاء الأوامر النشطة حتى 5 ثوانٍ قبل إغلاق سجل الأدوات (`Registry`). في Unix يوقف إلغاء `go_verify` مجموعة عملياته؛ لا يشمل ذلك عملية انفصلت إلى مجموعة أخرى، ولا يعدّ عزلاً أمنياً.

## الأمان

- **Jail**: `os.Root` + تحقق من المسار، ورفض symlinks في `edit_file`/`undo`، ورفض المسارات الحساسة: `.env*` في كل مكوّن (عدا ملفات `.env.example` و`.env.sample` و`.env.template` و`.env.dist` النهائية؛ لا يُفحص محتواها)، `.git/`، `.nabd/`، `id_*` (عدا `.pub`)، وملفات المفاتيح.
- على Linux/Android تُفتح ملفات القراءة وكل مكوّنات مسارها دون اتباع الروابط الرمزية؛ هذا لا يغلق سباقات المسار في كل عمليات الكتابة، ولا ينطبق الضمان نفسه على المنصات الأخرى.
- **Gate**: preview + diff + hashes، وtoken أحادي الاستخدام مربوط بالأداة و`args_sha256`، صلاحيته 5 دقائق. لا يمكن استدعاء `RunApproved` بدونه.
- **Audit**: أحداث `requested`/`approved`/`executing`/`denied`/`applied`/`failed` مع hashes وسلسلة `prev_hash`/`hash`. الكتابة إلزامية قبل تنفيذ الأدوات الكاتبة؛ فشل تسجيل النتيجة لا يمحو حدث `executing`.
- **go_verify وmcp_call**: محذوفتان افتراضياً. تُعرضان فقط عند توفّر `NABD_MCP_CONFIG` و`NABD_EXEC_SANDBOX=1` ونجاح فحص العزل؛ العزل يقيّد الملفات والشبكة والذاكرة والعمليات، ويُنظَّف عند الإلغاء. لا يُقدَّم هذا على Termux أو Windows أو أي منصة لا يمكن إثبات العزل عليها.
- **mcp-server**: لا يعرض أدوات التعديل والتنفيذ؛ قد يكتب سجل التدقيق عند القراءة.
- **CLI/TUI**: `process-group kill` على Unix/Termux، و`taskkill /T` كأفضل جهد على Windows.

راجع [docs/THREAT_MODEL.md](docs/THREAT_MODEL.md) لما يضمنه النظام وما لا يضمنه.

## دعم linux/arm64

تُعتبر معمارية `linux/arm64` (AArch64) منصة كاملة الدعم ومُتحقَّقاً منها وقت التشغيل على عتاد أصلي في الـ CI (`ubuntu-26.04-arm`)، وليست مجرد هدف بناء متقاطع:

- **المتطلبات الدنيا**:
  - نواة Linux ≥ 3.8 (لجدول استدعاءات `asm-generic/unistd.h`)، ويُفضَّل ≥ 4.18 لدعم namespaces المستقرة لـ bubblewrap.
  - المعمارية الدنيا المدعومة: `ARMv8.0-A` (مثل Cortex-A53 وما فوقها عبر `GOARM64=v8.0`).
  - حزم `bubblewrap` (bwrap) ومساحات أسماء المستخدمين غير المميزة (`unprivileged user namespaces`).
- **ما تم التحقق منه وقت التشغيل (Runtime Verified)**:
  - حارس `cmd/mcp-guard`: التحقق الصارم من معمارية `AUDIT_ARCH_AARCH64`، وقتل العملية فوراً عند عدم التطابق (`SECCOMP_RET_KILL_PROCESS`).
  - حظر استدعاءات `setsid` (رقم 157) و `setpgid` (رقم 154) بـ `EPERM`.
  - حظر البت 30 بـ `SECCOMP_RET_KILL_PROCESS`.
  - إثبات عزل الـ sandbox بـ bubblewrap (`sandbox.Prove`) على عتاد AArch64 حقيقي.
  - حل مكتبات ELF المحملة ديناميكياً عبر مسارات multiarch الخاصة بـ arm64 (`/lib/aarch64-linux-gnu` و `/usr/lib/aarch64-linux-gnu`).
- **الفروق المعروفة عن amd64**:
  - غياب استدعاءات النظام القديمة (`open`, `stat`, `fork`, `pipe`, `dup2`) واستبدالها بنظيراتها الحديثة (`openat`, `newfstatat`, `clone`, `pipe2`, `dup3`). راجع [docs/SECCOMP_ARCH.md](docs/SECCOMP_ARCH.md) لجدول التكافؤ الكامل.
  - **الذاكرة الافتراضية (RLIMIT_AS)**: مضبوطة افتراضياً على 2 GiB لجميع المعماريات، مع إمكانية الضبط الصريح عبر `NABD_SANDBOX_MEMORY_LIMIT` بين حد أدنى 512 MiB (لمنع انهيار وقت تشغيل Go) وحد أقصى 64 GiB، ويتم توثيق القيمة في سجل التدقيق عند تفعيل العزل.
  - **منصة Android / Termux**: رغم أن `GOOS=android` يطابق `linux` في Go، إلا أن عزل bubblewrap معطّل صراحة على Android (`!android`) وتفشل `sandbox.Prove` بشكل مغلق (`ErrUnavailable`).

## الاختبارات و CI

```bash
gofmt -l .          # يجب أن يكون فارغاً
go vet ./...
go test ./... -count=1
staticcheck ./...
```

`.github/workflows/ci.yml` يشغّل gofmt وvet و`go mod verify` والاختبارات و`go test -race -short` وstaticcheck بإصدار محدد وgovulncheck، ويثبّت bubblewrap ويشغّل اختبار إثبات العزل على Linux، ويبني `linux/amd64` و`linux/arm64` و`android/arm64` و`windows/amd64` بـ`CGO_ENABLED=0` على كل push إلى `main`/`audit/**` وكل PR. البناء المتقاطع واختبار Linux لا يُغنيان عن تشغيل الاختبارات على Termux أو Windows فعلياً.

## القيود والمخاطر المعروفة

- حد وسائط الموافقة ومعاينتها 4000 بايت؛ قد يُرفض تعديل صغير إذا اضطر إلى تضمين سطر طويل جداً، حتى لو لم يمكن تقسيمه.
- تعطيل `mcp_call` و`go_verify` هو الافتراضي؛ إعادة تفعيلهما تتطلب تهيئة مستخدم وإثبات عزل ناجح في هذه المرة التشغيلية، وليس فقط نجاح البناء أو الاختبار.
- الموافقة على `mcp_call` مرتبطة ببصمة الخادم والحارس والبيئة المسموح بها، ويُعاد التحقق من البصمات قبل التنفيذ؛ يبقى نافذ قصير يمكن فيه مستخدم محلي تبديل ملف بعد التحقق، ولا يوجد `fexecve` في هذه الأداة لإغلاقه تماماً.
- استثناءات `.env` للملفات النهائية فقط: مجلد باسم `.env.example` محجوب، حتى لو كان اسمه شبيهاً باسم قالب مسموح.
- على Windows لم تُختبر وقت التشغيل معالجة النقاط والمسافات النهائية في مكوّنات المسار مقابل قواعد الأسماء الحساسة؛ لا تعتمد على المنع الاسمي وحده هناك.
- CI يثبت إصدار staticcheck ويفحص الثغرات باستخدام govulncheck؛ يجب إبقاء أداة Go والتبعيات محدّثتين.

- حدود الحجم: قراءة حتى 3584 بايت لكل صفحة (قد تتمدّد حتى ثلاثة بايتات لإكمال محرف UTF-8)، تعديل ونسخته الاحتياطية 2MiB، وسجل `go_verify` يحتفظ بـ64KiB من بدايته ونهايته. تُعرض حتى 1000 نتيجة من أول 4096 مدخلاً مفحوصاً من المجلد، مع تنبيه عند بلوغ حد المسح؛ الترتيب يخص الجزء المفحوص فقط.
- Shadow: آخر 20 نسخة لكل مسار و7 أيام؛ يُشغَّل تنظيف دوري أثناء حياة العملية (افتراضياً كل 24 ساعة)، لذا الموعد مضمون فقط طالما العملية تعمل. لا ينسّق مع عمليات Nabd الأخرى أو كاتب خارجي، ولا يوجد ضمان حذف بعد إيقاف العملية. فشل حذف ملف يُبلَّغ ولا يُفقد النسخ الحديثة قدرة الاستعادة. الكتابة عبر rename تفصل hard links.
- Audit: السلسلة تكشف التعديل والحذف وإعادة الترتيب فقط إذا كانت المرساة `NABD_AUDIT_ANCHOR` محفوظة خارج متناول الخصم. مستخدم محلي يملك السجل والمرساة يستطيع إعادة حسابها. لا يوجد تدوير أو توقيع خارجي بعد.
- عند تفعيل العزل يعمل التنفيذ داخل user/mount/pid/net namespace مع root شبه فارغ، و`RLIMIT_AS`/`RLIMIT_NPROC`، ومع `--die-with-parent` وقتل المجموعة عند الإلغاء. لا يُقدَّم هذا العزل على Windows أو Termux أو أي منصة لا تتوفر فيها أدوات التحقق؛ الحارس وseccomp و`os.Root` ليست بديلاً.
- يعرض الوكيل **كل** ناتج أداة مع الوجهة والوسائط بصيغة ASCII مقتبسة، ويطلب التمرير إلى النهاية والموافقة قبل إرساله إلى النموذج. كشف الأسرار بالمحتوى إرشادي فقط، يفوت صيغاً غير متوقعة وقد يُنبّه على نص عادي؛ ليس ضماناً. بيانات ردود النموذج نفسها تخرج إلى Endpoint كما هي.
- قفل الملف يمنع عمليتي Nabd من التحرير المتزامن، لكنه ليس CAS ذرياً مقابل كاتب خارجي؛ بين الفحص النهائي وإعادة التسمية تبقى نافذة سباق. على الأنظمة غير Unix لا تُزامَن إعادة التسمية مع الدليل.
- لم تُكتمل اختبارات التشغيل على جهاز Termux أو Windows فعلي؛ البناء المتقاطع لا يُعدّ اختبار تشغيل.
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
