# نموذج التهديدات — NabdCoder

هذا المستند يصف ما يحاول NabdCoder حمايته، ومن هم الخصوم، وما يضمنه الكود في هذا المستودع فعلياً، وما لا يضمنه. يصف حالة الكود عند إصدار `v0.1.0-alpha`، لا نوايا تصميمية غير منفذة. كل ادعاء هنا يجب أن يقابله مسار في الشيفرة أو اختبار مذكور بالاسم.

الإصدار التجريبي لا يعني ضمان أمان شامل ولا دعم منصة لم تُختبر تشغيلياً.

## 1. النطاق

يشمل:

- وكيل `cmd/agent` وأدواته في `internal/tools`.
- خادم `cmd/mcp-server` (للقراءة فقط).
- التخزين المحلي: `.nabd/shadow` و`.nabd/audit.log`.
- عميل LLM في `internal/llm` وبيئة التشغيل.
- حدود العزل الاختيارية في `internal/sandbox` ومساعديها `cmd/nabd-limit` و`cmd/nabd-sandbox-probe` و`cmd/mcp-guard`.

لا يشمل:

- أمان نظام Termux/Android نفسه أو صلاحيات التطبيق.
- أمان خادم LLM البعيد أو مزوّد الشبكة أو مزوّد البحث.
- أمان مستودع Git البعيد أو حسابات المطوّرين.
- عزل kernel/container خارج ما يثبته `sandbox.Prove` على Linux.

## 2. الأصول

- **ملفات المشروع**: سرية وسلامة (كود، إعدادات، بيانات).
- **الأسرار**: مفاتيح API، `.env*`، `.git/config`، ملفات المفاتيح، متغيرات البيئة.
- **نسخ shadow وسجل التدقيق**: قابلية الاستعادة وسلامة السجل.
- **الطرفية**: منع تزوير الواجهة أو مطالبات الموافقة.
- **الجهاز**: الذاكرة والقرص (Termux محدود الموارد).

## 3. حدود الثقة

1. **المستخدم ↔ TUI**: المستخدم موثوق؛ الطرفية قد تكون غير موثوقة إذا كانت مشتركة.
2. **TUI ↔ endpoint LLM**: الـendpoint غير موثوق؛ كل رسالة تُرسل قد تحمل محتوى workspace.
3. **النموذج ↔ الأدوات**: النموذج غير موثوق؛ قد يكون مخترقاً أو متأثراً بـprompt injection.
4. **الأدوات ↔ workspace**: المسارات والملفات غير موثوقة؛ قد تحتوي symlinks أو محتوى مضللاً.
5. **`go_verify` ↔ كود المشروع**: كود الاختبار غير موثوق.
6. **`mcp_call` ↔ عمليات خارجية**: binary خارجي غير موثوق، مُعرَّف مسبقاً من المستخدم فقط.
7. **`mcp-server` ↔ MCP client**: العميل غير موثوق؛ الخادم يقيّد نفسه للقراءة فقط.

## 4. الخصوم والقدرات

- **نموذج مخترق أو prompt injection**: يطلب أي أداة، يصوغ الوسائط، ويخفي تعليمات داخل الملفات أو مخرجات الأدوات.
- **ملفات مشروع خبيثة**: ANSI/control sequences، مسارات ملتوية، symlinks، أسماء أسرار، مخرجات مضللة.
- **مستخدم محلي آخر**: يقرأ/يحذف `.nabd`، ويعرف مسارات الملفات، بلا صلاحيات مرتفعة.
- **endpoint بعيد**: يرى الرسائل كاملة وقد يحتفظ بها.
- **عملية MCP خارجية**: قد تنفّذ أي شيء عند تشغيلها.
- **كود اختبار خبيث**: يُنفَّذ عبر `go_verify`.

## 5. ما يضمنه النظام

### 5.1 احتواء المسارات

- `os.Root` على جذر الـworkspace لكل عمليات الملفات، مع رفض `..` والمسارات المطلقة وvolume-relative وNUL.
- `create_file` و`edit_file` و`undo` يرفضون symlinks (بما فيها الروابط المعلّقة عند الهدف والمجلدات الوسيطة لـ`create_file` مع التحقق بـ`O_NOFOLLOW` ومطابقة `SameFile` على Unix).
- حدود إنشاء المجلدات الأب في `create_file`: حد أقصى 15 مستوى أب، و512 بايت للمسار النسبي، و255 بايت لكل مكوّن، مع تراجع بأفضل جهد عن المجلدات التي أنشأتها العملية وحدها عند فشل الكتابة قبل النشر.
- على Linux/Android تُفتح ملفات القراءة وكل مكوّنات مسارها بـ`O_NOFOLLOW`.
- **حدود**: هذا لا يجعل كل عمليات الكتابة مقاومة لسباقات الروابط أو إعادة تسمية المجلدات، ولا ينطبق الضمان نفسه على المنصات الأخرى.

### 5.2 سياسة المسارات الحساسة

مرفوضة لأدوات الملفات ومخفية من `list_dir`: `.nabd/`، `.git/`، `.env*` في كل مكوّن (عدا أسماء القوالب النهائية `.env.example` و`.env.sample` و`.env.template` و`.env.dist`، ولا يُفحص محتواها)، `id_*` (عدا `.pub`)، والامتدادات `.pem` و`.key` و`.p12` و`.pfx` و`.jks` و`.keystore`.

هذه سياسة **بالاسم فقط**: سرّ داخل `config.yaml` أو ملف لا يطابق الأنماط يمكن قراءته.

### 5.3 بوابة الموافقة

- `create_file` تعرض المحتوى الكامل مع ترويسة المسار والحجم وSHA-256، وتربط الرمز بحالة عدم الوجود وبصمة المحتوى الكامل؛ يفشل مغلقاً إذا وُجد الملف مسبقاً أو ظهر في الفاصل، ويحترم `umask` بلا بت تنفيذ، ولا ينشئ نسخة `shadow`.
- `edit_file` يعرض diff وSHA-256 للمحتوى قبل/بعد، ويُعاد فحص hash الأساس قبل التنفيذ.
- `RunApproved` يتطلب token أحادي الاستخدام مربوطاً بالأداة و`args_sha256` (و`identity` لـ`mcp_call`/`web_fetch`/`web_search`)، صالح 5 دقائق.
- `Run` بدون approver يفشل مغلقاً.
- **استثناء إلزامي**: `go_verify` و`mcp_call` و`web_search` و`web_fetch` تتطلب موافقة تنفيذ بشرية في كل وضع، بما في ذلك `NABD_AUTO_APPROVE=all` و`/auto all`. الإنفاذ في نقطة واحدة داخل السجل (`Registry.RequiresManualApproval` + `approveWithState`)، لا في نص الواجهة.
- **`web_search`**: تُطبَّع الوسائط مرة واحدة (`CanonicalWebSearch`) وتُستخدم في المعاينة والهوية والتنفيذ. الهوية `websearch:sha256:...` تغطي نسخة التطبيع والمزوّد ومضيف الوجهة والاستعلام و`max_results`؛ مفاتيح API ليست جزءاً منها ولا تُعرض. تغيير أي حقل بعد الموافقة يُبطل الـtoken. تُعرض كل وحدات الاستعلام بلفّ صريح (72 محرفاً، حتى 12 سطراً) ويُرفض ما يتجاوز الحد بدل قصّه، ويُفحص النص **المفكوك** لرفض C0/C1 وbidi وzero-width وUnicode Tags ولو وصلت كـ`\uXXXX` في JSON. هذا يمنع إخفاء جزء من الاستعلام أو إعادة ترتيب عرضه، لكنه لا يمنع المستخدم من الموافقة على استعلام ضار واضح المحتوى.
- تغيير وضع الموافقة لا يفوض الطلبات المعلقة بأثر رجعي: الدفعة تحتفظ بالوضع الذي أُدرجت فيه، ويُشترط أن يكون الوضع الحالي أيضاً مسموحاً.

**ضمان محصور**: هذا يخص الأداتين المذكورتين فقط. الأدوات الأخرى قد تشغّل عمليات فرعية وفق سياساتها المعلنة.

### 5.4 الإفصاح عن مخرجات الأدوات

في TUI، كل ناتج أداة يُعرض مع الوجهة والوسائط، ويُطلب التمرير إلى النهاية والموافقة قبل إرساله إلى النموذج، ما لم يكن الوضع التلقائي يسمح بذلك.

**حدود `cmd/mcp-server`**: لا يعرض أدوات التعديل أو التنفيذ، لكنه أيضاً **لا يمرّ عبر بوابة الإفصاح هذه**. يُرجع مخرجات أدوات القراءة مباشرة إلى عميل MCP المتصل؛ عرضها للمستخدم أو تمريرها إلى نموذج مسؤولية ذلك العميل. لا يوجد ضمان أن العميل يعرض البايتات كاملة أو يطلب موافقة. الخادم يقرأ إعداداته لمعرفة المفاتيح المتاحة للقراءة، وقد ينشئ `.nabd/audit.lock` ويكتب `.nabd/audit.log` عند القراءة؛ لا يمنح ذلك العميل كتابة عبر الأدوات المعروضة. فتح سجل موجود يتطلب القفل، وعلى نظام ملفات للقراءة فقط يفشل `Open` مغلقاً بدل التحقق بلا قفل.

- `readonly` يُفصح تلقائياً عن مخرجات أدوات القراءة **التي لا يطابقها كاشف الأسرار الإرشادي**.
- `all` يُفصح تلقائياً عن كل شيء.
- الكاشف الإرشادي ليس ماسح أسرار: يفوت صيغاً غير متوقعة وقِيَماً مُرمَّزة ومفاتيح مقسّمة، وقد ينبّه على نص عادي. **غياب التحذير لا يثبت خلو المحتوى من الأسرار**، والضمان الحقيقي هو مراجعة المستخدم للبايتات المعروضة.

### 5.5 تدقيق إلزامي للكتابة

`requested`/`approved` fail-closed قبل التنفيذ؛ تعذّر الكتابة يمنع التعديل/التنفيذ. القراءة تعمل بدون تدقيق. كل حدث يحمل بصمات (SHA-256) ووسائط نصية مُطهّرة، وسلسلة `prev_hash`/`hash`.

**تسلسل الكتابة**: كل إضافة تأخذ قفلاً استشارياً على `.nabd/audit.lock` (flock على Unix، `LockFileEx` على Windows) بمهلة 10 ثوانٍ ثم تفشل مغلقاً. تحت القفل تُعاد قراءة حجم السجل، ويُرفض التقلّص، ويُتحقق من هوية الملف (dev/inode أو volume/file index) ومن بصمة آخر سطر؛ أي سجلات أضافتها عملية أخرى تُتحقق وتُضاف إلى السلسلة قبل الحدث الجديد. تُزامَن بيانات الحدث (`fsync`) قبل إعلان النجاح، ويُزامَن دليل `.nabd` عند إنشاء السجل على Unix.

**حدود الكشف**: السلسلة تكشف التعديل أو الحذف أو إعادة الترتيب في وسط السجل فقط إذا كانت المرساة `NABD_AUDIT_ANCHOR` محفوظة خارج متناول الخصم. **اقتطاع ذيل السجل لا يكشفه السجل نفسه**: بدون مرساة خارجية تحفظ آخر `hash` معروف، لا يمكن التمييز بين «السجل ينتهي هنا» و«حُذف ذيله». القفل يمنع فقط الكتابة المتعاونة التي تحترمه، لا كاتباً يملك الملف. **تعديل بادئة بنفس الحجم** مع إعادة `mtime` لا يغيّر حجم السجل ولا الذيل، فلا يكشفه مسار الإضافة المتزايد؛ يكشفه `Open`/`Verify` الكامل أو مرساة خارجية. لا يوجد تدوير أو توقيع خارجي بعد.

### 5.6 تخزين shadow

- نسخة قبل كل تعديل في `.nabd/shadow`، واستعادة عبر `undo` بعد التحقق من `sha256_after`.
- الاحتفاظ: آخر 20 نسخة لكل مسار و7 أيام، مع تنظيف دوري أثناء حياة العملية (افتراضياً كل 24 ساعة).
- **الحذف قابل للاستئناف**: تُحذف البيانات قبل metadata، فأي انقطاع يترك أثراً معروفاً (metadata بلا نسخة) ينهيه المسح التالي. النسخ اليتيمة وملفات `.tmp-*` تُكنس بعد مهلة سماح (15 دقيقة) لتجنّب سباق كتابة من عملية أخرى.
- `Latest` يتخطى النسخة المفقودة أو التالفة ولا يعيدها كناجحة؛ وإن لم توجد نسخة سليمة يُرجع خطأً صريحاً.
- **ترتيب المتانة**: تُكتب بيانات النسخة وتُزامَن ثم يُزامَن دليل المخزن، ثم تُكتب metadata وتُستبدل بـrename ويُزامَن الدليل مرة أخرى؛ لا يُعلن `Backup` النجاح قبل اكتمال ذلك. على Unix فشل `sync` مطلوب يفشل العملية (fail-closed). على Windows حيث لا يتوفر `fsync` للدليل يُستكمل التشغيل مع قيد موثّق وتحذير في `/status`؛ قد لا تنجو أحدث نسخة من انقطاع كهرباء مفاجئ، وهذا مقبول صراحةً بدل تعطيل التعديل على Windows أو ادعاء متانة غير موجودة.
- فشل التنظيف لا يُسقط تعديلاً ناجحاً، لكنه يبقى ظاهراً في `LastCleanupError` وسطر `/status` حتى ينجح مسح كامل، ويُقاس بعدّاد فشل متتالٍ. إذا حُذفت البيانات ثم فشل `sync` الدليل، يُرجع التنظيف خطأً ويسجّله.
- **حدود**: لا تنسيق بين عمليات Nabd متعددة ولا مع كاتب خارجي، ولا ضمان حذف بعد إيقاف العملية، والكتابة عبر rename تفصل hard links. مزامنة الدليل تحمي من انقطاع العملية/الطاقة، لا من كاتب يملك المجلد.

### 5.7 العزل الاختياري (Linux فقط)

`go_verify` و`mcp_call` معطّلتان افتراضياً. تُعرضان فقط عند تحقق كل ما يلي في نفس التشغيل: `NABD_MCP_CONFIG` صالح، و`NABD_EXEC_SANDBOX=1`، ونجاح `sandbox.Prove`.

Profile المُنفَّذ على Linux:

- bubblewrap مع `--unshare-user --unshare-pid --unshare-net --unshare-ipc --unshare-uts --new-session --die-with-parent --clearenv`.
- جذر شبه فارغ، `/proc` وtmpfs `/tmp`.
- `/workspace` قابل للكتابة لـ`go_verify` وللقراءة فقط لـ`mcp_call`.
- مسارات الحساسة داخل `/workspace` تُخفى: المجلدات بـtmpfs فارغ، والملفات بربط `/dev/null` (قراءتها تنجح لكنها **فارغة**، لا غير قابلة للقراءة).
- `RLIMIT_AS` و`RLIMIT_NPROC` عبر `cmd/nabd-limit`، مع `NABD_SANDBOX_MEMORY_LIMIT` في مجال [512MiB, 64GiB].
- بيئة allowlist بلا مفاتيح: `GOPROXY=off`, `GOFLAGS=-mod=readonly`, `GOTOOLCHAIN=local`, `CGO_ENABLED=0`.

### 5.8 ما يثبته `Prove` بالضبط

`Prove` يشغّل `cmd/nabd-sandbox-probe` داخل العزل نفسه ويفشل مغلقاً إن لم يتحقق كل ما يلي:

1. مساحة أسماء الشبكة مختلفة عن المضيف (يُمرَّر `EXPECT_HOST_NETNS` ويُقارن من داخل العزل).
2. مستمع loopback على المضيف **يعمل** من خارجه (ضابط إيجابي) و**لا يُبلغ** من داخل العزل.
3. `/etc/passwd` غير مرئي.
4. الكتابة في منطقة للقراءة فقط تفشل.
5. قيم `RLIMIT_AS` و`RLIMIT_NPROC` المرئية داخل العزل تساوي القيم المطلوبة.

**ما لا يثبته `Prove`**، ويجب عدم ادعائه:

- أنه يشغّل profile الكتابة (`WritableWorkspace: true`) بنفس الضمانات؛ هو يثبت profile قراءة فقط.
- أن مسارات الحساسة داخل `/workspace` مخفية فعلاً؛ هذا يثبته اختبار التكامل `TestGoVerifySandboxHidesWorkspaceSecrets` عند تشغيله.
- استهلاك الذاكرة أو العمليات الفعلي؛ يثبت **ضبط** الحدود لا أن الكود لا يتجاوزها عبر قنوات أخرى.
- كل خصائص profile الإنتاج: `go_verify` يضيف روابط فردية لمُحمّل ELF والمكتبات، ويختلف عن profile الإثبات.
- أن العزل يمنع كل كتابة خارج `/workspace`: `/tmp` جزء من profile القابل للكتابة.
- أن العملية المنفصلة عن مجموعة العمليات لا تفلت عند الإلغاء.

## 6. ما لا يضمنه النظام

- **لا ضمان لعزل كود الاختبار بلا `Prove` ناجح**: إن لم يُثبت العزل تبقى الأداتان غير موجودتين؛ لا يوجد مسار تنفيذ غير معزول في الإنتاج.
- **شبكة كود الاختبار**: `GOPROXY=off` يمنع تنزيل وحدات Go فقط؛ في غياب العزل يستطيع كود الاختبار فتح sockets.
- **`mcp_call`**: الحارس seccomp يحجب `setsid` و`setpgid` (بعد التحقق من المعمارية وضبط البت 30)، وهذا لا يمنع كل الانفصالات ولا يعد عزلاً للملفات أو الشبكة بحد ذاته. هوية الملف التنفيذي والحارس مرتبطة بالموافقة، ويبقى نافذ قصير يمكن فيه مستخدم محلي تبديل ملف بعد التحقق (لا `fexecve`).
- **تسريب المحتوى إلى LLM**: أي أداة تُفصح عن مخرجاتها ترسلها للـendpoint؛ إن كان بعيداً خرجت البيانات من الجهاز.
- **إحصاءات الكاش**: أرقام `Cache` في `/status` هي ما يبلّغ عنه المزوّد فقط؛ قد تكون غائبة أو جزئية أو خاصة بنسخة/إعداد، ولا تثبت إصابة كاش ولا تُترجم إلى وفورات. لا تُستخدم لأي قرار أمني أو مالي. في `llama.cpp` قد يعكس `cache_n` تطابق بادئة عميل آخر على slot مشترك، فلا يُفسَّر كضمان خصوصية.
- **TOCTOU داخل الجذر**: بين preview والتنفيذ قد يتغيّر محتوى الملف؛ hash guard يخفف ولا يوجد قفل CAS ذري مقابل كاتب خارجي. في `create_file` يُستعمل `RENAME_NOREPLACE` (على Linux/Android) أو `os.Root.Link` (على باقي المنصات) للنشر الحصري الذري؛ أما على أنظمة الملفات التي لا تدعمهما (`ENOTSUP`/`ENOSYS`/`EPERM` مثل `/sdcard` أو FAT32) فتتراجع الأداة إلى فحص `Lstat` أخير داخل `withProcessLock` ثم `Rename` (بينما يبقى `EEXIST` فشلاً مغلقاً دائماً)، ما يترك نافذة سباق ميكروثانية أمام كاتب خارجي في تلك الأنظمة فقط.
- **حدود الحجم**: صفحة قراءة 3584 بايت، إنشاء/تعديل ونسخة shadow الاحتياطية 2MiB، سجل `go_verify` يحتفظ بـ64KiB من بدايته ونهايته، ووسائط الموافقة ومعاينتها 4000 بايت، وعمق مجلدات `create_file` حتى 15 مستوى أب (512 بايت للمسار النسبي).
- **لا حدود معدل أو حصص** لاستدعاءات الأدوات أو الـLLM.

## 7. فروق المنصات

| الجانب | Linux | Windows | Termux/Android |
|---|---|---|---|
| عزل التنفيذ | bubblewrap + user namespaces، يُثبت بـ`Prove` | غير متاح (`ErrUnavailable`) | غير متاح: الملف مبني بـ`linux && !android` |
| `go_verify`/`mcp_call` | متاحتان فقط بعد إثبات ناجح | معطّلتان دائماً | معطّلتان دائماً |
| فتح الملفات بلا اتباع روابط | `O_NOFOLLOW` لكل مكوّن | غير مطبق | مطبق |
| قتل شجرة العمليات | process group | `taskkill /T` أفضل جهد | process group |
| مزامنة دليل shadow/audit | `fsync` إلزامي وfail-closed | غير مدعومة؛ يُستكمل التشغيل مع تحذير `/status` وقيد موثّق | `fsync` إلزامي |
| اختبار التشغيل | CI على `amd64` و`arm64` (يجب أن يفشل عند التخطي) | لم يُثبت؛ حزمة `internal/safefs` و`internal/tools` تفشل مسبقاً في اختباراتها | غير مثبت |

## 8. سيناريوهات واختبارات

- هروب مسار/abs/UNC/NUL → `TestJailRejectsEscapes`, `TestResolveSafePathRejectsEscapes`.
- symlink خارج الجذر أو بمكوّن وسيط/معلّق → `TestJailRejectsSymlinkEscape`, `TestEditThroughSymlinkedParentRejected`, `TestCreateFileSecurityRejections`.
- إنشاء ملفات جديدة وسلامة المجلدات الأب والتراجع وTOCTOU → `TestCreateBugRegressionEditMissingFile`, `TestCreateFileInEmptyWorkspaceAndSubdirs`, `TestCreateFileTOCTOUAppearsBetweenPreviewAndExec`, `TestCreateFileContentAndPathBinding`, `TestCreateFileRejectsExistingFile`, `TestCreateFileGoSyntaxValidation`, `TestCreateFileConcurrentSamePath`, `TestCreateFileRollbackCreatedParentsOnFailure`.
- قراءة أسرار بالاسم → `TestSensitivePathsDenied`.
- تجاوز البوابة بلا approver → `TestRunFailsClosedWithoutApprover`.
- تجاوز الموافقة عبر الوضع التلقائي → `TestAutoApprovalIsRefusedForManualApprovalTools`, `TestAutoApproveAllDoesNotBypassManualTools`.
- تفويض رجعي عند تغيير الوضع → `TestModeChangeDoesNotAuthorizeQueuedTools`.
- تأكيد `/auto all` → `TestAutoAllRequiresExplicitConfirmation`.
- إعادة استخدام token أو استخدامه لوسائط أخرى → `TestApprovalTokenRequiredAndSingleUse`.
- انتهاء token → `TestApprovalTokenExpires`.
- تعذّر التدقيق → `TestAuditLogFailClosedForWrites`.
- تزوير ANSI → `TestSanitizeForDisplay`, `TestApprovalPreviewSanitizesANSIText`.
- تسريب بيئة الاختبار → `TestGoVerifyEnvAllowlist`.
- حجب ملفات الـworkspace داخل العزل → `TestGoVerifySandboxHidesWorkspaceSecrets`.
- عزل الشبكة/الملفات/الحدود → `TestLinuxSandboxProof`, `TestLinuxSandboxOptInPath`.
- فشل التنظيف والاستئناف → `TestCleanupKeepsMetadataWhenBackupRemovalFails`, `TestCleanupRepairsMetadataWithoutBackup`, `TestLatestSkipsUnusableNewestEntry`, `TestCleanupSweepsOrphanedBackupAndTempFiles`, `TestCleanupFailureIsReportedAndCleared`.
- ترتيب متانة النسخ وفشل المزامنة → `TestBackupSyncsDataThenMetadata`, `TestBackupFailsClosedWhenRequiredSyncFails`, `TestCleanupReportsDirectorySyncFailureAndRecovers`.
- تزامن سجل التدقيق بين عمليتين → `TestConcurrentProcessesShareOneChain` (عمليتان عبر `os/exec` في وضعي sha256 وHMAC)، ورفض الاقتطاع/الاستبدال/السطر الناقص → `TestWriteRefusesTruncationReplacementAndPartialTail`، وقفل/روابط/ـFIFO → `TestAuditLockRejectsSymlinksAndNonRegularFiles`, `TestAuditLockRejectsFIFO`.
- هوية البحث والموافقة اليدوية → `TestCanonicalWebSearchIdentityIsStableAndFieldBound`, `TestWebToolsRequireManualApprovalInEveryMode`, `TestWebSearchApprovalTokenIsBoundToEveryField`, `TestWebSearchApprovalPreviewShowsCompleteCanonicalRequest`, `TestWebSearchQueryRejectsInvisibleAndControlCharacters`, `TestWebSearchLongUnicodeQueryIsShownFullyWithoutClipping`.
- ثبات بادئة الطلب → `TestOpenAIToolsPrefixIsDeterministic`, `TestOpenAIRequestPrefixIsDeterministicAndAppendOnly`, `TestGeminiRequestPrefixIsDeterministicAndAppendOnly`.
- قراءة usage والكاش وعرضه → `TestOpenAIUsageCacheFieldParsing`, `TestGeminiUsageCacheFieldParsing`, `TestCacheStatsUsesSumsNotAverageOfRatios`, `TestCacheStatsCoverageInvalidAndNoData`, `TestLateLLMResponseDoesNotPolluteCacheStats`, `TestCacheStatsResetOnClear`, `TestStatusShowsCacheWithoutConversationText`.
- حد مسار الكنس → `TestCleanupNeverTouchesFilesOutsideTheStore`.
- undo بعد تغيير الملف → `TestUndoRefusesAfterDrift`.

## 9. المخاطر المتبقية

- **P1**: إثبات سلوك profile الكتابة ومسارات الحساسة في CI مع فشل صريح عند التخطي؛ وتشغيل الاختبارات على Termux فعلياً.
- **P2**: تدوير سجل التدقيق وتوقيع خارجي؛ ومرساة تحفظ آخر hash لكشف اقتطاع الذيل وكشف إعادة كتابة البادئة بنفس الحجم دون الاعتماد على `mtime`.
- **P2**: تنقيح الأسرار بالمحتوى بدل الاسم.
- **P3**: Job Objects على Windows بدل `taskkill`، و`fexecve` لإغلاق نافذة استبدال الملف.

## 10. التحقق على Termux

راجع قسم «التحقق على Termux» في [README](../README.md): كتلة probe واحدة مع المخرجات المتوقعة لـDNS/TLS/HTTPS. نتيجة هذا الفحص لم تُثبت بعد، والبناء المتقاطع `android/arm64` لا يُعد اختبار تشغيل.
