# دليل تكامل خادم REA MCP المعزول (Reverse Engineer Anything) — NabdCoder

## 1. نظرة عامة والهدف

يوفر NabdCoder تكاملاً اختيارياً ومعزولاً (Opt-in Sandboxed MCP Integration) لربط الوكيل مع خادم MCP الخاص بمشروع [morluto/rea](https://github.com/morluto/rea) (REA: Reverse Engineer Anything)، لتمكين الوكيل من فحص الملفات الثنائية، فك التجميع (Disassembly)، فك الترجمة (Decompilation)، وتحليل البنى البرمجية دون المساس بأمن النظام المضيف.

يخضع التكامل لضوابط NabdCoder الصارمة:
- **معطّل افتراضياً (Opt-in only)**: لا يعمل خادم REA تلقائياً؛ يجب على المستخدم تمكينه صراحة بضبط متغيرات البيئة.
- **صفر تضخم في الثنائي (Zero Binary Bloat)**: لا يحتوي ثنائي NabdCoder على أي حزم Node.js أو كود مدمج لـ REA.
- **عزل أمني كامل (Full Process Isolation)**: عزل صارم عبر الحارس والتقييد الصارم لصلاحيات القراءة، وحظر كامل للشبكة.
- **فشل مغلق (Fail-Closed)**: أي خطأ في الإعدادات، أو غياب بصمة التجزئة SHA-256، أو محاولة تمرير مسارات نسبية، أو استخدام مشغلات الحزم الديناميكية يؤدي إلى رفض التشغيل فوراً.

---

## 2. تثبيت الحزمة وتثبيت سلسلة التوريد (Supply-Chain Pinning)

لحماية النظام من هجمات سلسلة التوريد (Supply-Chain Attacks)، يجب تثبيت إصدار ثابت من خادم REA وحساب بصمة SHA-256 الخاصة به:

### التثبيت:
```bash
# تثبيت إصدار ثابت محدد (مثال: 6.10.1)
npm install -g rea-agents@6.10.1
```

### حساب بصمة التجزئة (SHA-256):
- **على Linux / Termux:**
  ```bash
  sha256sum $(which rea-mcp || which rea)
  ```
- **على Windows (PowerShell):**
  ```powershell
  (Get-FileHash -Algorithm SHA256 (Get-Command rea).Source).Hash.ToLower()
  ```

> [!IMPORTANT]
> يُمنع منعاً باتاً استخدام مشغلات الحزم الديناميكية مثل `npx` أو `npm exec` أو `yarn dlx` لتفادي هجمات تنزيل كود غير موثوق به وقت التشغيل.

---

## 3. متغيرات البيئة والإعداد (Environment Variables)

لتفعيل التكامل، اضبط المتغيرات التالية قبل تشغيل NabdCoder:

| المتغير | النوع | الحالة | الوصف |
| :--- | :--- | :--- | :--- |
| `NABD_REA_MCP_BIN` | مسار مطلق | إلزامي للتفعيل | المسار الكامل للملف التنفيذي لخادم REA. يرفض النظام أي مسار نسبي. |
| `NABD_REA_MCP_SHA256` | نص ست عشري (64 محرفاً) | إلزامي | بصمة SHA-256 للملف التنفيذي لمطابقتها قبل التشغيل. |
| `NABD_REA_TARGET_DIR` | مسار مطلق | إلزامي | المسار الكامل لمجلد العينات المراد تحليلها (يُربط كـ read-only فقط). |
| `NABD_REA_MCP_GUARD` | مسار مطلق | اختياري/تلقائي | مسار حارس العمليات `nabd-mcp-guard` (يُكتشف تلقائياً بجانب الثنائي). |
| `NABD_REA_MCP_GUARD_SHA256` | نص ست عشري | اختياري | بصمة SHA-256 لحارس العمليات للتحقق الإضافي. |

### مثال الإعداد والتشغيل (Linux / Bash):
```bash
export NABD_REA_MCP_BIN="/usr/local/bin/rea"
export NABD_REA_MCP_SHA256="e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
export NABD_REA_TARGET_DIR="/home/user/analysis/samples"

./nabd-agent
```

---

## 4. حدود العزل والأمان (Security Invariants)

1. **تقييد القراءة فقط لمجلد العينات (Read-Only Target Bind):**
   - يتم تمرير مجلد العينات `NABD_REA_TARGET_DIR` داخل بيئة العزل كـ Read-Only Bind (`ro-bind` في Bubblewrap).
   - لا يستطيع خادم REA تعديل أو حذف أو إنشاء أي ملف داخل مجلد العينات أو نظام الملفات.

2. **حظر الوصول للشبكة (Zero Network Egress):**
   - تعمل العملية المعزولة بفك ارتباط الشبكة (`unshare-net`)، مما يمنع تسريب أي شيفرة أو بيانات مفككة خارج الجهاز.

3. **سقف الاستجابة المخصص (32 KiB Response Ceiling):**
   - خادم REA يمتلك سقف استجابة يبلغ `32 KiB` (`maxREAMCPResponseBytes = 32768`) بدلاً من السقف العام (`8 KiB`)، لتوفير مساحة كافية لمخرجات الهندسة العكسية المفصلة.
   - يتم قص البيانات الزائدة مع حماية المحارف متعددة البايت (UTF-8 Boundary Safe Truncation) وإضافة وسم `...[mcp output truncated]...`.

4. **تطهير المخرجات (Sanitization):**
   - كل النصوص المرتجعة من خادم REA تخضع لدالة `sanitizeForDisplay` لإزالة أي تسلسلات ANSI أو محارف تحكم طرفية أو أحرف ثنائية الاتجاه (Unicode Bidi) خبيثة قد تستغل تزييف المخرجات.
   - تُعامل مخرجات الأدوات كـ `untrusted data` معزولة في سجل الرسائل بدور `tool` ولا يمكنها تجاوز قيود النظام أو حقن توجيهات للنموذج.

5. **الموافقة الإلزامية والإفصاح:**
   - تتطلب أداة `mcp_call` موافقة تفاعلية مسبقة من المستخدم (`Approve mcp_call? [Y/N]`).
   - يُعرض ملخص الاستدعاء مسبقاً، وتخضع النتيجة لمراجعة الإفصاح قبل إرسالها إلى مزود LLM.

---

## 5. واجهة الاستخدام (TUI) وأوامر المساعدة

- **أمر `/status`**:
  - إذا كان التكامل معطلاً:
    ```
    REA Reverse Engineering: Disabled (opt-in via NABD_REA_MCP_BIN)
    ```
  - إذا كان مفعّلاً:
    ```
    REA Reverse Engineering: Enabled (bin: /usr/local/bin/rea, target: /home/user/analysis/samples)
    ```
- **أمر `/tools`**:
  - يوضح تفعيل أدوات الهندسة العكسية عبر أداة `mcp_call`:
    ```
    • REA Reverse Engineering tools are active via mcp_call(command: "rea", tool: "<name>")
    ```
- **ملخصات الاستدعاء وتوسيع المخرجات (Ctrl+O)**:
  - تظهر استدعاءات أدوات REA بالصيغة الذكية: `rea:<tool>(...) -> N bytes`.
  - الضغط على `Ctrl+O` يتيح توسيع واستعراض المخرجات المطهرة حتى 4000 محرف في بطاقة الواجهة.
