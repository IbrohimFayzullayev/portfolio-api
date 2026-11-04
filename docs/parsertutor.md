# Parser o'qituvchisi (parsertutor)

Moliya botining birinchi AI agenti. U real foydalanishda parser nimani noto'g'ri o'qiganini topadi, katalogga eng kichik o'zgarishni taklif qiladi va bu o'zgarishni odamlar yozgan har bir qatorda sinab ko'radi.

## Ishlash tartibi

```
finbot bazasi (faqat o'qish)
   │  entries (source='bot'), parse_failures
   ▼
namunalar ── niqoblash (raqam → 9, ism → Ism)
   │
   ├─▶ model: so'z (stem) taklif qiladi ──┐
   │                                      │ evaluate: hamma qatorni oldin/keyin o'qish
   │   ◀───────── natija ─────────────────┘
   ▼
finish: birorta qatorni buzgan taklif rad etiladi
   ▼
hisobot.md: catalog.go va parse_test.go uchun tayyor qatorlar
   ▼
Ibrohim ko'rib chiqadi → commit → CI
```

Vazifalar quyidagicha taqsimlangan:

- **Model faqat taklif qiladi.** U niqoblangan matnni ko'radi: summa, ism va user id unga bormaydi.
- **Kod qaror qiladi.** Taklif xotirada qo'llanadi va haqiqiy parser hamma qatorda ishga tushiriladi. Odamlar qabul qilgan bitta qatorni buzgan taklif ham o'tmaydi.
- **Odam merge qiladi.** Agent bazaga ham, repoga ham yozmaydi.

### Yorliqlar qayerdan keladi

| Manba       | Ma'nosi                                                                                                 | Kuchi                                                          |
| ----------- | ------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------- |
| `corrected` | Bot yozgan, keyin egasi tahrirlagan qator. Saqlangan holati egasi nazarda tutgan narsa                  | Kuchli (oltin)                                                 |
| `accepted`  | Bot yozgan va hech kim o'zgartirmagan qator. "Boshqa" kategoriyasi bundan mustasno: u "bilmadim" degani | O'rtacha (kumush)                                              |
| `failure`   | Bot umuman o'qiy olmagan xabar                                                                          | Yorliqsiz. Endi o'qiladigan bo'lsa, odam ko'z bilan tekshiradi |

## Maxfiylik

- Agent faqat `FINBOT_ADMIN_SEES_MONEY=true` bo'lganda ishlaydi. Bot foydalanuvchiga nima va'da qilgan bo'lsa, shunga amal qiladi: "bot egasi ko'rmaydi" deyilgan paytda bu asbob ham ularning ma'lumotini o'qimaydi.
- Bazadagi rol **ustun darajasida** cheklangan: `user_id`, `note` va jadvaldagi boshqa ustunlarga kirish yo'q. Agent kim nima yozganini umuman bilmaydi.
- Modelga faqat niqoblangan matn boradi.
- Test qatorlarini model o'zi to'qiydi. Real xabarlar testga tushmaydi, chunki repo ochiq. Kod buni tekshiradi.
- **Ochiq beta oldidan:** do'stlarga "tushunilmagan xabarlar niqoblangan holda AI'ga yuboriladi" deb aytib qo'ying.

## Sozlash (bir marta)

Read-only rol yarating (serverda):

```sh
cd /opt/portfolio
docker compose -f docker-compose.prod.yml exec db psql -U portfolio -d finbot
```

```sql
CREATE ROLE finbot_tutor LOGIN PASSWORD '<uzun-tasodifiy-parol>';
GRANT CONNECT ON DATABASE finbot TO finbot_tutor;
GRANT USAGE ON SCHEMA public TO finbot_tutor;
GRANT SELECT (raw_text, kind, category, person, amount_minor, currency,
              created_at, updated_at, source) ON entries TO finbot_tutor;
GRANT SELECT (text, reason, created_at) ON parse_failures TO finbot_tutor;
```

Server `.env` fayliga qo'shing:

```
FINBOT_TUTOR_DATABASE_URL=postgres://finbot_tutor:<parol>@db:5432/finbot?sslmode=disable
ANTHROPIC_API_KEY=sk-ant-...
# ixtiyoriy, standart qiymat claude-sonnet-5-5
# TUTOR_MODEL=
```

## Ishga tushirish

Avval modelsiz ishlating. Bu bepul va parser holatini ko'rsatadi:

```sh
cd /opt/portfolio
# `run -e VAR` qiymatni .env dan emas, shell'dan oladi. Kalit bot konteyneriga
# doimiy qo'shilmaydi, faqat shu bir martalik ishga tushirishda ishlatiladi.
set -a && . ./.env && set +a
docker compose -f docker-compose.prod.yml run --rm --no-deps \
  -e FINBOT_TUTOR_DATABASE_URL -e ANTHROPIC_API_KEY -e TUTOR_MODEL \
  --entrypoint /app/parsertutor finbot -offline -out - > tutor-$(date +%F).md
```

To'liq ishga tushirish uchun `-offline` ni olib tashlang. `finbot` servisi orqali ishga tushirilgani uchun `FINBOT_ADMIN_SEES_MONEY` botdagi bilan bir xil bo'ladi.

Lokal kompyuterda (SSH tunnel bilan): `go run ./cmd/parsertutor -days 30 -out report.md`.

## Hisobot bilan ishlash

1. "Takliflar" jadvalini o'qing. Har bir so'z (stem) yonida nimani tuzatgani yozilgan.
2. "Boshqacha o'qiladigan" va "Endi o'qiladigan" bo'limlarini ko'z bilan ko'rib chiqing. Ularning to'g'riligini yorliq hal qilmaydi.
3. Tayyor qatorlarni `catalog.go` va `parse_test.go` ga qo'shing, keyin `go test ./internal/financetext/` ni ishlating.
4. "Modelning izohlari" bo'limidagi takliflar, masalan yangi kategoriya yoki `notNames`, sizning qaroringiz.

## Avtonomiya bosqichlari

| Bosqich       | Kim boshlaydi       | Natija                                           | O'tish sharti                                    |
| ------------- | ------------------- | ------------------------------------------------ | ------------------------------------------------ |
| **1 (hozir)** | Ibrohim qo'lda      | Hisobot fayli                                    | 3 hafta ketma-ket takliflar foydali chiqsa       |
| 2             | Cron, har yakshanba | Hisobot outbox orqali Telegram'ga keladi         | Rad etilgan takliflar 20% dan kam bo'lsa         |
| 3             | Cron                | Branch va PR. CI testlaydi, Ibrohim merge qiladi | Bot tushunmagan xabarlar ulushi barqaror pasaysa |

Agent hech qachon o'zi merge qilmaydi va bazaga yozmaydi.

## O'lchovlar

- Tushunilmagan xabarlar ulushi. Admin paneldagi bu ko'rsatkich hozir 20% dan oshsa sariq bilan belgilanadi.
- Yorliqli qatorlarning to'g'ri o'qilish foizi (hisobotning "Holat" bo'limi).
- Har bir ishga tushirishdagi token sarfi (hisobot oxirida).
