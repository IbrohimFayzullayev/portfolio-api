# Portfolio MCP server

Claude (Claude Code yoki Claude Desktop) portfolio bilan to'g'ridan-to'g'ri ishlashi uchun MCP server. U kontentni tahlil qiladi va tahrirlaydi, xabarlar, tizim holati, moliya va o'sish bo'yicha javob beradi.

## Qanday ishlaydi

```
Siz (chat) ──▶ Claude Code / Desktop ──▶ Claude modeli
                    │  ▲                   (qaysi tool kerakligini hal qiladi)
          stdio     ▼  │
              portfolio-mcp ──HTTP, Bearer──▶ Go API ──▶ PostgreSQL
              (cmd/portfolio-mcp)             (admin panel ishlatadigan API'ning o'zi)
```

- **MCP server bazaga kirmaydi.** U admin panel bilan bir xil API'ni chaqiradi. Shuning uchun validatsiya, `translation_key` qoidalari va nashrdagi ops-bot xabarnomasi xuddi paneldagidek ishlaydi.
- **Autentifikatsiya mavjud yo'l orqali.** Server admin login va paroli bilan kirib, Bearer token oladi. API'ga yangi kirish yo'li qo'shilmaydi.
- **Protokol qo'lda yozilgan** (`internal/mcp`). Bu stdio transportidagi JSON-RPC, faqat standart kutubxona ishlatiladi. Shu qatlamning o'zi Amanat pilotida ham ishlatiladi.

## Tool'lar

| Tool                                             | Nima qiladi                                                                                                                                                     | Turi            |
| ------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------- |
| `content_audit`                                  | Hamma post va loyihani tekshiradi: uz/en jufti, eski qoralamalar, bo'sh tavsif, qisqa matn, demo qoldiqlar, `example.com` kabi shablon havolalar, repo/url yo'qligi, "muammo → qaror → natija" shakli | o'qish          |
| `list_posts`, `get_post`                         | Ro'yxatda matn o'rniga so'zlar soni qaytadi, to'liq matn faqat bitta post uchun olinadi                                                                         | o'qish          |
| `create_post_draft`                              | Faqat qoralama yaratadi                                                                                                                                         | yozish          |
| `update_post`                                    | Faqat berilgan maydonlarni o'zgartiradi, nashr holatiga tegmaydi                                                                                                | yozish          |
| `set_post_published`                             | Postni nashr qiladi yoki nashrdan oladi. Client har safar sizdan so'raydi (`destructiveHint`)                                                                   | ommaviy         |
| `link_translations` | uz va en versiyani bitta `translation_key` bilan bog'laydi. To'qnashuvni oldindan tekshiradi. `content_audit` juft bo'lishi mumkin bo'lganlarni `pair_suggestions` ostida ko'rsatadi | yozish |
| `schedule_post` | Qoralamani belgilangan vaqtda (Toshkent vaqti) nashr qilishga rejalashtiradi yoki rejani bekor qiladi. Nashrni ops bot scheduler'i bajaradi. Client ruxsat so'raydi | ommaviy |
| `list_projects`, `get_project`, `update_project` | Loyihalar bilan postlardagi kabi ishlaydi                                                                                                                       | o'qish / yozish |
| `list_messages`, `mark_message_read`             | Aloqa formasidan kelgan xabarlar                                                                                                                                | o'qish / yozish |
| `list_invitations`                               | Taklifnoma saytidan kelgan javoblar                                                                                                                             | o'qish          |
| `system_overview`                                | Uptime, xatolar, deploy, outbox, bot, backup                                                                                                                    | o'qish          |
| `list_changes` | Agent nimani o'zgartirgani: tool, element, har bir maydonning oldingi va keyingi qiymati, xato urinishlar. Qaytarish uchun ham shu | o'qish |
| `log_practice` | Ingliz tili mashqini yozadi: `/qildim` sintaksisi (`45 w essay`) yoki tur va daqiqalar | yozish |
| `add_finance_entry` | Moliya yozuvini qo'shadi. Matnni bot bilan bir xil parser o'qiydi (`taksi 25k`). `dry_run` rejimi faqat qanday o'qilishini ko'rsatadi, saqlamaydi | yozish |
| `finance_summary`                                | Shaxsiy moliya (sizniki, finbot foydalanuvchilariniki emas)                                                                                                     | o'qish          |
| `growth_summary`                                 | Ingliz tili mashqlari, sprintlar, IELTS mock natijalari                                                                                                         | o'qish          |

Ataylab qo'shilmagan tool'lar:

- **O'chirish.** Kerak bo'lsa, admin panelda qilinadi.
- **Finbot foydalanuvchilari ma'lumoti.** Do'stlarning ma'lumoti o'zlariga tegishli.

Tayyor so'rovlar (prompts). Claude Code'da `/mcp__portfolio__...` ko'rinishida chiqadi:

- `haftalik_xulosa`: oxirgi 7 kun bo'yicha xulosa va 3 ta keyingi qadam.
- `loyihani_qayta_yoz slug`: loyihani case study shakliga keltirish, uz va en versiyalarida.
- `post_qoralama mavzu`: ikki tilli post qoralamasi.

## Agent jurnali

MCP orqali qilingan har bir yozish amali `agent_changes` jadvaliga yoziladi (migratsiya 000017). Jurnalga quyidagilar tushadi: qaysi tool ishlatilgani, qaysi element o'zgargani, maydonlarning oldingi va keyingi to'liq qiymatlari, API amalni qabul qilgan yoki qilmagani. Xato bilan tugagan urinishlar ham yoziladi. Yozuvni MCP serverning o'z kodi qo'shadi, model emas. Shuning uchun birorta o'zgarish jurnaldan chetda qolmaydi.

- **Ops bot xabari.** Har bir o'zgarishda xabar keladi, masalan `🤖 MCP: update_post`, element va o'zgargan maydonlar bilan. Xatolar ⚠️ belgisi bilan keladi. Xabarni o'qilgan qilish faqat jurnalga yoziladi, xabar yuborilmaydi.
- **Ko'rish va qaytarish.** Claude'ga "nima o'zgartirding?" yoki "oxirgi o'zgarishni qaytar" deb yozing. U `list_changes` dagi oldingi qiymatlarni olib, ularni qayta yozadi.
- **API.** `GET/POST /api/v1/agent-changes` (faqat admin). `actor` maydoni keyinroq Telegram'dagi `/ai` buyrug'i uchun ham ishlatiladi.
- **Jurnal yozilmay qolsa**, o'zgarish baribir amalga oshgan bo'ladi. Tool javobida `audit_error` chiqadi va Claude buni sizga aytadi.

## Token tejash qanday ishlaydi

Model o'qigan har bir bayt token hisoblanadi. Shuning uchun tool'lar API javobini qisqartirib qaytaradi:

- **Ro'yxatlar:** post matni o'rniga so'zlar soni.
- **Moliya:** kunlik qatorlar va bo'sh oylar olib tashlanadi. Real API'da 874 baytdan 335 baytgacha qisqardi.
- **O'sish:** mashq qilinmagan kunlar olib tashlanadi. 1465 baytdan 150 baytgacha.
- **Audit:** hisob-kitob serverda bajariladi, model faqat topilgan muammolar ro'yxatini ko'radi.

Tool ta'riflari (taxminan 7 KB) har bir so'rovga qo'shiladi. Bu MCP'ning o'z narxi.

## Ulash

### 1. Serverda (bir marta)

Patch qo'llangach, CI yangi image'ni deploy qiladi. `/app/portfolio-mcp` binari `api` konteyneri ichida bo'ladi.

`/opt/portfolio/.env` fayliga qo'shing:

```
PORTFOLIO_API_EMAIL=<admin email>
PORTFOLIO_API_PASSWORD=<admin parol>
```

### 2. Kompyuteringizda: Claude Code

MCP server SSH orqali, ishlab turgan `api` konteyneri ichida ishga tushadi. Yangi port ochilmaydi va internetga yangi endpoint chiqmaydi. Kirish SSH kalitingiz orqali bo'ladi.

```sh
claude mcp add portfolio -- ssh -T -o BatchMode=yes ibrohim@<server> \
  'cd /opt/portfolio && set -a && . ./.env && set +a && \
   docker compose -f docker-compose.prod.yml exec -T \
     -e PORTFOLIO_API_URL=http://localhost:8080 \
     -e PORTFOLIO_API_EMAIL="$PORTFOLIO_API_EMAIL" \
     -e PORTFOLIO_API_PASSWORD="$PORTFOLIO_API_PASSWORD" \
     api /app/portfolio-mcp'
```

Tekshirish: `claude mcp list` (holati `connected` bo'lishi kerak), keyin Claude Code ichida `/mcp`.

`exec` ishlatilganining sababi: u ishlab turgan konteynerga ulanadi. Shuning uchun `.env` dagi eski `TAG=latest` muammosi bu yerda ta'sir qilmaydi.

### 3. Yoki Claude Desktop

`claude_desktop_config.json` fayliga:

```json
{
  "mcpServers": {
    "portfolio": {
      "command": "ssh",
      "args": [
        "-T",
        "-o",
        "BatchMode=yes",
        "ibrohim@<server>",
        "cd /opt/portfolio && set -a && . ./.env && set +a && docker compose -f docker-compose.prod.yml exec -T -e PORTFOLIO_API_URL=http://localhost:8080 -e PORTFOLIO_API_EMAIL=\"$PORTFOLIO_API_EMAIL\" -e PORTFOLIO_API_PASSWORD=\"$PORTFOLIO_API_PASSWORD\" api /app/portfolio-mcp"
      ]
    }
  }
}
```

### Lokal ishlab chiqishda

```sh
make run   # API :8080
PORTFOLIO_API_EMAIL=... PORTFOLIO_API_PASSWORD=... go run ./cmd/portfolio-mcp
```

Bu buyruq stdio'da JSON-RPC kutadi. Uni qo'lda emas, `claude mcp add portfolio-local -- go run ./cmd/portfolio-mcp` orqali ulang.

## Birinchi sinov uchun so'rovlar

- "Kontent auditini qil va eng muhim 3 ta ishni ayt."
- "O'qilmagan xabarlar bormi? Qisqacha ayt."
- "Radius CRM loyihasini case study shakliga keltiramiz." Yoki `/mcp__portfolio__loyihani_qayta_yoz radius-crm`.
- "Shu oy nimaga eng ko'p pul ketdi, o'tgan oy bilan solishtir."
- "Bu hafta ingliz tilida qancha ishladim?"

## Keyingi qadamlar

- **Telefondan ishlatish.** Buning uchun Claude ilovasida custom connector kerak, ya'ni Streamable HTTP transporti va OAuth. Shu bosqichda `internal/mcp` rasmiy Go SDK'ga almashtiriladi, tool'lar esa o'zgarmaydi.
- **Parsertutor'ni ham tool qilish** (`tutor_brief`, `tutor_evaluate`). Shunda parserni yaxshilash ishi ham chatdan, obuna limiti ichida bajariladi.
