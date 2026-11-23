# نموذج التهديدات — NabdCoder

تحديث هذا الفرع في القسم الأخير يقيّد أو يصحح وصف الأدوات الداخلية أدناه؛ التنفيذ الخارجي معطّل في الوكيل.

هذا المستند يحدد ما يحاول NabdCoder حمايته، ومن هم الخصوم، وما الذي يضمنه النظام فعلياً، وما لا يضمنه. يعتمد على الكود والاختبارات في هذا المستودع، وليس على نوايا تصميمية غير منفذة.

## 1. النطاق

يشمل:

- وكيل `cmd/agent` وأدواته في `internal/tools`.
- خادم `cmd/mcp-server` (للقراءة فقط).
- التخزين المحلي: `.nabd/shadow` و`.nabd/audit.log`.
- عميل LLM في `internal/llm` وبيئة التشغيل.

لا يشمل:

- أمان نظام Termux/Android نفسه، أو صلاحيات التطبيق.
- أمان خادم LLM البعيد أو مزوّد الشبكة.
- أمان مستودع Git البعيد أو حسابات المطوّرين.
- عزل على مستوى kernel/container؛ لا يوجد sandbox.

## 2. الأصول

- **ملفات المشروع**: سرية وسلامة (كود، إعدادات، بيانات).
- **الأسرار**: `NABD_API_KEY`, متغيرات البيئة، ملفات المفاتيح، `.env`, `.git/config`.
- **سجل التدقيق ونسخ shadow**: سلامة السجل وقابلية الاستعادة.
- **الطرفية**: منع تزوير الواجهة أو مطالبات الموافقة.
- **الجهاز**: الذاكرة والقرص (Termux محدود الموارد).

## 3. حدود الثقة

1. **المستخدم ↔ TUI**: المستخدم موثوق؛ الطرفية قد تكون غير موثوقة إذا كانت مشتركة.
2. **TUI ↔ endpoint LLM**: الـendpoint غير موثوق؛ كل رسالة تُرسل قد تحمل محتوى workspace.
3. **النموذج ↔ الأدوات**: النموذج غير موثوق؛ قد يكون مخترقاً أو متأثراً بـprompt injection.
4. **الأدوات ↔ workspace**: المسارات والملفات غير موثوقة؛ قد تحتوي symlinks أو محتوى مضللاً.
5. **go_verify ↔ كود المشروع**: كود الاختبار غير موثوق؛ ينفّذ بصلاحيات المستخدم.
6. **mcp_call ↔ عمليات خارجية**: binary خارجي غير موثوق؛ يعمل بصلاحيات المستخدم.
7. **mcp-server ↔ MCP client**: العميل غير موثوق؛ الخادم يقيّد نفسه للقراءة فقط.

## 4. الخصوم والقدرات

- **نموذج مخترق أو prompt injection**: يستطيع طلب أي أداة، وصياغة وسائط، وإخفاء تعليمات داخل الملفات أو مخرجات الأدوات.
- **ملفات مشروع خبيثة**: ANSI/control sequences، مسارات ملتوية، symlinks، أسماء أسرار، مخرجات أدوات مضللة.
- **مستخدم محلي آخر**: يقرأ/يحذف `.nabd`، يعرف مسارات الملفات، ولا يملك elevated privileges.
- **endpoint بعيد**: يرى الرسائل كاملة، وقد يحتفظ بها.
- **عملية MCP خارجية**: قد تنفّذ أي شيء عند تشغيلها، بما في ذلك الوصول للشبكة.
- **كود اختبار خبيث**: يُنفَّذ عبر `go_verify` ويمكنه قراءة الملفات وفتح sockets.

## 5. ما يضمنه النظام

- **احتواء المسارات**: `os.Root` على جذر الـworkspace لكل عمليات الملفات، مع تحقق إضافي من `..` والمسارات المطلقة وvolume-relative وNUL. `edit_file` و`undo` يرفضان symlink. (اختبارات: `TestJailRejectsEscapes`, `TestJailRejectsSymlinkEscape`, `TestEditThroughSymlinkedParentRejected`.)
- **فتح القراءة على Linux/Android**: فتح نسبي إلى واصف الجذر، مع `O_NOFOLLOW` لكل مكوّن وفتح غير حاجب. هذا لا يجعل جميع عمليات الكتابة مقاومة لسباقات الروابط أو إعادة تسمية المجلدات؛ لا يُدّعى الضمان نفسه للمنصات الأخرى.
- **سياسة المسارات الحساسة**: `.nabd/`, `.git/`, `.env*` في كل مكوّن (عدا ملفات `.env.example` و`.env.sample` و`.env.template` و`.env.dist` النهائية؛ لا يُفحص محتواها), `id_*` (عدا `.pub`), وملفات المفاتيح مرفوضة لأدوات الملفات، ومخفية من `list_dir`. (اختبارات: `TestSensitivePathsDenied`, `TestShadowDeniedToFileTools`.)
- **بوابة الموافقة**: preview يعرض المسار وdiff وSHA-256، وإعادة فحص hash الأساس قبل التنفيذ. `RunApproved` يتطلب token أحادي الاستخدام مربوطاً بالأداة و`args_sha256`، صالح 5 دقائق. `Run` بدون approver يفشل مغلقاً. (اختبارات: `TestApprovalPreviewEdit`, `TestApprovalGuardDetectsFileChange`, `TestRunFailsClosedWithoutApprover`, `TestApprovalTokenRequiredAndSingleUse`, `TestApprovalTokenExpires`.)
- **تدقيق إلزامي للكتابة**: `requested`/`approved` fail-closed قبل التنفيذ؛ تعذّر الكتابة يمنع التعديل/التنفيذ. القراءة تعمل بدون تدقيق. (اختبارات: `TestAuditLogFailClosedForWrites`, `TestReadOnlyToolsSurviveAuditFailure`, `TestAuditLogApprovalFlow`.)
- **تعديل آمن**: كتابة ذرّية temp+fsync+rename مع حفظ mode، رفض no-op وsymlink والملفات >2MiB، CRLF محفوظ، ونسخة shadow قبل التعديل. (اختبارات: `TestEditAtomicPreservesModeAndLeavesNoTemp`, `TestEditPreservesCRLFInGo`, `TestEditRejectsSymlink`, `TestEditRejectsLargeFile`.)
- **استعادة محكومة**: `undo` يستعيد فقط إذا طابق محتوى الملف `sha256_after`، ويرفض symlink، ويعلّم النسخة `undone`. (اختبارات: `TestUndoRestoresFile`, `TestUndoRefusesAfterDrift`, `TestUndoNoArgRestoresLatest`, `TestUndoIsAudited`.)
- **go_verify محكوم**: بيئة allowlist بلا مفاتيح أو proxy، `GOFLAGS=-mod=readonly`, `GOPROXY=off`, `GOTOOLCHAIN=local`، أنماط حزم workspace-relative، مهلة 90s توقف مجموعة العمليات على Unix؛ قد تفلت العمليات المنفصلة ولا يضمن ذلك عزل الكود، ونتيجة منظمة. (اختبارات: `TestGoVerifyEnvAllowlist`, `TestValidatePkgPattern`, `TestGoVerifyTimeout`, `TestProcessGroupKill` على Unix.)
- **LLM مضبوط**: إعداد من البيئة فقط، مهلة كلية، إلغاء عند Ctrl+C، retry محدود على 429/5xx/الشبكة فقط، وميزانية سياق تقديرية مع trim واعٍ للأزواج. (اختبارات: `TestAgentCancelsRequestOnCtrlC`, `TestChatRetriesOn429And5xx`, `TestChatDoesNotRetry400`, `TestTrimHistoryKeepsToolPairs`.)
- **واجهة مقاومة للتزوير**: إزالة ANSI/C0/C1 قبل العرض، clamp للطرفية، وخروج عند non-TTY. (اختبارات: `TestSanitizeForDisplay`, `TestApprovalPreviewSanitizesANSIText`, `TestTinyWindowDoesNotPanic`, `TestEnsureInteractiveRejectsNonTTY`.)
- **حماية MCP الخارجي**: الحارس المطلوب بجانب الوكيل يحجب `setsid` و`setpgid` على Linux/Android المدعومين، وتُحجب البيئة إلا قائمة محددة ويُحد النقل الوارد بـ1MiB. هذه ليست عزلة ملفات أو شبكة. (اختبارات: `TestF24RealMCPCallAndEnvironment`, `TestF24SeccompBlocksSetsid`, `TestF24SeccompBlocksSetpgid`, `TestF24WireLimit`.)
- **mcp-server للقراءة فقط**: يرفض الأدوات غير read-only لغياب approver، و`mcp_call` معطّل. (اختبار: `TestServerIsReadOnlyAndDisablesMCPCall`.)

## 6. ما لا يضمنه النظام

- **لا sandbox**: `go_verify` و`mcp_call` ينفّذان كوداً/عمليات بصلاحيات المستخدم. الموافقة خطوة بشرية، وليست عزلاً.
- **شبكة كود الاختبار**: `GOPROXY=off` يمنع تنزيل وحدات Go فقط؛ كود الاختبار يستطيع فتح sockets مباشرة.
- **`mcp_call`**: يشغّل binary بصلاحيات المستخدم، بلا sandbox للملفات أو الشبكة؛ `command` لا يدعم وسائط. seccomp يمنع syscallين فقط، ولا يضمن منع كل الانفصالات أو الهجمات عبر namespaces أو خدمات خارجية. لا يعتمد على `NABD_MCP_GUARD` لاستبدال الحارس. هوية الملف التنفيذي والحارس غير مرتبطة بالموافقة؛ يمكن تبديلهما بعد عرض الأمر وقبل التنفيذ.
- **تسريب المحتوى إلى LLM**: `read_file` يتطلب موافقة قبل إرسال الناتج؛ قد تكشف أدوات أخرى والرسائل المكتوبة بيانات للـendpoint. إذا كان بعيداً، تخرج البيانات المسموح بها من الجهاز.
- **سياسة الأسرار بالأسماء فقط**: سر داخل `config.yaml` أو ملف غير مطابق للأنماط يمكن قراءته.
- **سلامة سجل التدقيق**: append-only بالبناء، لكنه ليس tamper-evident؛ مستخدم محلي يستطيع حذف/تفريغ `.nabd/audit.log`. لا hash chain ولا تدوير بعد.
- **TOCTOU داخل الجذر**: بين preview والتنفيذ قد يتغيّر محتوى الملف داخل الـworkspace. hash guard + التطابق الفريد يخففان، لكن لا يوجد قفل ملف.
- **hard links**: الكتابة الذرّية عبر rename تستبدل الاسم؛ الأسماء الأخرى المرتبطة تحتفظ بالمحتوى القديم.
- **Windows**: `taskkill /T` أفضل جهد، والصلاحيات approximated.
- **Termux DNS/TLS**: غير مؤكدين لثنائي `CGO_ENABLED=0`؛ راجع فحص الجهاز في README.
- **لا حدود معدل/حصص**: لا rate limiting لاستدعاءات الأدوات أو الـLLM.
- **لا LICENSE**: لم يُضف ترخيص قانوني بعد.

## 7. سيناريوهات واختبارات

- **هروب مسار/abs/UNC/NUL** → `TestJailRejectsEscapes`.
- **symlink خارج الجذر** → `TestJailRejectsSymlinkEscape` و`TestEditThroughSymlinkedParentRejected`.
- **قراءة أسرار بالاسم** → `TestSensitivePathsDenied`.
- **تجاوز البوابة بلا approver** → `TestRunFailsClosedWithoutApprover`.
- **إعادة استخدام token أو استخدامه لوسائط أخرى** → `TestApprovalTokenRequiredAndSingleUse`.
- **انتهاء token** → `TestApprovalTokenExpires`.
- **تعذّر التدقيق** → `TestAuditLogFailClosedForWrites`.
- **تزوير ANSI** → `TestSanitizeForDisplay` و`TestApprovalPreviewSanitizesANSIText`.
- **طرفية صغيرة/non-TTY** → `TestTinyWindowDoesNotPanic` و`TestEnsureInteractiveRejectsNonTTY`.
- **تسريب بيئة الاختبار** → `TestGoVerifyEnvAllowlist`.
- **أنماط حزم خارجية** → `TestValidatePkgPattern`.
- **تعليق شجرة عمليات** → `TestProcessGroupKill` (Unix) و`TestGoVerifyTimeout` (الكل).
- **حلقة أدوات لا نهائية/متكررة** → `TestMaxIterationsSynthesizesToolErrors` و`TestRepeatedCallsBlocked`.
- **إلغاء طلب LLM** → `TestAgentCancelsRequestOnCtrlC`.
- **retry غير صحيح** → `TestChatRetriesOn429And5xx` و`TestChatDoesNotRetry400`.
- **كسر أزواج tool calls** → `TestTrimHistoryKeepsToolPairs`.
- **تعديل غير ذرّي/فقدان mode** → `TestEditAtomicPreservesModeAndLeavesNoTemp`.
- **undo بعد تغيير الملف** → `TestUndoRefusesAfterDrift`.
- **خادم MCP يعدّل ملفاً** → `TestServerIsReadOnlyAndDisablesMCPCall`.

## 8. المخاطر المتبقية وأولوياتها

- **P1**: عزل/allowlist لـ`mcp_call`، وsandbox أو تقييد شبكي/ملفي لـ`go_verify` إذا كان النموذج غير موثوق.
- **P2**: سلسلة hash (`prev_hash`/`entry_hash`) وتدوير سجل التدقيق.
- **P2**: كشف/تنقيح الأسرار بالمحتوى لا بالاسم.
- **P2**: `os.Root` + قفل/معاملة تغلق TOCTOU بين preview والتنفيذ.
- **P3**: Job Objects على Windows بدل `taskkill`، وضبط `max_tokens`/`temperature`، وstreaming، وترخيص.

## 9. التحقق على Termux

راجع قسم «التحقق على Termux» في [README](../README.md): كتلة probe واحدة مع المخرجات المتوقعة لـDNS/TLS/HTTPS. النتيجة تُحدد ما إذا كان `CGO_ENABLED=0` يحتاج ضبط `SSL_CERT_FILE` أو مسار DNS محلي.


## تحديث أمني: فرع المعالجة

- تُزال أداتا `go_verify` و`mcp_call` من أدوات الوكيل عند بدء التشغيل لأن عزلهما غير كافٍ. لا ينبغي تشغيل التنفيذ الداخلي مباشرة على مشروع أو خادم غير موثوق.
- قبل إرسال ناتج `read_file` إلى النموذج، يعرض الوكيل الوجهة والوسائط والمخرجات بصيغة مقتبسة، ويطلب التمرير إلى النهاية والموافقة. لا تكشف السياسة الأسرار حسب المحتوى، ولا تحجب الرسائل التي يكتبها المستخدم أو بيانات الأدوات الأخرى.
- معاينة التعديل والكتابة تستخدمان التحويل نفسه؛ تُحفظ البايتات خارج نطاق الاستبدال، وتُفحص صياغة Go دون إعادة تنسيق تلقائية. ترتبط الموافقة ببصمة الناتج أيضاً.
- تُنسق التعديلات والاستعادة داخل Registry واحد. يُفتح المجلد الأب مرة واحدة وتُجرى القراءة النهائية والكتابة والاستبدال بالنسبة إليه، ثم تُزامن بيانات المجلد على Unix؛ تعذر المزامنة بعد الاستبدال يُبلغ كتحذير بعد نجاح الاستبدال. لا يضمن ذلك atomic compare-and-swap مقابل كاتب خارجي، ولا ينسق عمليات Nabd المتعددة؛ ولا تتوفر مزامنة المجلد في هذا التنفيذ على الأنظمة الأخرى.
- سجل التدقيق ليس محصناً من العبث، وقد يفشل تسجيل النتيجة بعد تنفيذ التعديل. يحتفظ ببعض الوسائط النصية. تنظيف Shadow يتم عند إنشاء نسخة جديدة فقط؛ مدة السبعة أيام ليست ضمان حذف.
- خادم MCP المحلي لا يعرض التعديل والتنفيذ، لكن القراءة قد تكتب سجل التدقيق.
