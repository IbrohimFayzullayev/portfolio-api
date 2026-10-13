# Bot buyruqlari

Hamma statistika, reja va manbalar botdagi **Hisobot** tugmasida ochiladi: Pul · O'sish · Kitoblar · Hujjatlar. Hujjatlar ro'yxatini botda `/hujjat` buyrug'i bilan ham ochish mumkin.

## Ingliz tili — sprintlar

Streak ham, kunlik kvota ham yo'q. Har sprintda bitta natija jumlasi bo'ladi va oxirida bitta savol beriladi: natijaga erishdimmi?

| Buyruq | Nima qiladi |
|--------|-------------|
| `/sprint 5 ta Task 2 essay, 2 tasi tekshirilgan` | Sprint ochadi. Du–Ju kunlari bugundan, dam olish kunlari keyingi dushanbadan boshlanadi; shanba kuni tugaydi |
| `/sprint` | Joriy sprint: kun, ishlangan kunlar, daqiqalar, yozuvlar |
| `/qildim 45 w essay 2` | Mashg'ulotni yozish |
| `/qildim kecha 30 s` | Kechagi kun uchun yozish |
| `/qildim bekor` | Oxirgi yozuvni o'chirish (24 soat ichida) |
| 🎙 ovozli xabar | Speaking sifatida saqlanadi; sprint oxirida eshittiriladi |
| `/bugun` | Bugungi yozuvlar va sprint holati |
| `/ball 6.0 L6.5 R6 W5.5 S5.5 mock C18T1` | Mock yoki rasmiy ball (`rasmiy`, `tutor`) |
| `/yakun [izoh]` | Sprintni yopish: natijalar, birinchi/oxirgi ovoz va «Natijaga erishdingizmi?» ✅ 🟡 ❌ |
| `/solishtir` | Birinchi va oxirgi ovozli yozuv |

**Turlar:** `w` writing · `s` speaking · `l` listening · `r` reading · `v` vocab/anki · `g` grammar · `m` mock. Tur yozilmasa `other` bo'ladi.

**Vaqt:** `45` · `45daq` · `1s` · `1.5soat`.

Sprint ochiq bo'lgan ish kunida soat 21:00 gacha hech narsa yozilmagan bo'lsa, bot bitta eslatma yuboradi. Yakshanba kuni eslatma yo'q.

## Kitoblar

| Buyruq | Nima qiladi |
|--------|-------------|
| `/kitob yangi O'tkan kunlar - Abdulla Qodiriy 420 bet` | Yangi kitob qo'shish (muallif va betlar soni ixtiyoriy) |
| `/kitob 40` | Joriy kitobni 40 daqiqa o'qidim |
| `/kitob 40 120-bet` | ... va 120-betga yetdim |
| `/kitob ufq 30` | Bir nechta kitob bo'lsa, nomidan bir so'z qo'shiladi |
| `/kitob kecha 1s` | Kechagi kun uchun yozish |
| `/kitob tugatdim` · `/kitob tashladim` | Kitobni yopish |
| `/kitob bekor` | Oxirgi yozuvni o'chirish |
| `/kitob` | O'qilayotgan kitoblar |
| `/kitoblar` · `/kitoblar 2025` · `/kitoblar hammasi` | Statistika |

Kitob o'qish ingliz tili sprintiga qo'shilmaydi: bu dam olish, mashq emas.

## Pul

Pul uchun buyruq kerak emas, shunchaki yozing: `tushlik 35k`, `kecha taksi 18000`, `+10mln oylik`.

Asosiy buyruqlar: `/balans`, `/hisob`, `/qarzlar`, `/oxirgi`. To'liq ro'yxat: `/help`.

Muhim: buyruqsiz yozilgan har qanday matn pul yozuvi deb tushuniladi. Shuning uchun ingliz tili va kitoblar har doim buyruq bilan yoziladi.
