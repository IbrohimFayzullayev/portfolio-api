package portfoliomcp

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ibrohimcoder/portfolio-api/internal/mcp"
)

// instructions reach the model once, when the client connects. They are the
// house rules; the tool descriptions say what each tool does.
const instructions = `Bu Ibrohim Fayzullayevning portfolio ekotizimi: fayzullayev.uz (ikki tilli blog va loyihalar), admin panel, Go API, ops Telegram bot. Tool'lar shu API orqali ishlaydi - admin panel bilan bir xil qoidalar va tekshiruvlar.

Qoidalar:
- Javoblar o'zbek tilida (lotin yozuvi). Texnik atamalar asl holicha.
- Raqamlarni faqat tool natijasidan ol va qaysi davr ekanini ayt. Natijada bo'lmagan raqamni taxmin qilma.
- Nashr qilish, nashrdan olish va nashrni rejalashtirish (set_post_published, schedule_post) faqat Ibrohim shu suhbatda aniq so'raganda. Vaqtlar Toshkent vaqtida. Qoralama yaratish va tahrirlash mumkin.
- Katta matnni o'zgartirishdan oldin taklifni ko'rsat va tasdiq kut.
- Kontent ikki tilli: har bir post va loyihaning uz va en versiyasi bir xil translation_key bilan bog'lanadi. Bog'lash uchun link_translations; content_audit dagi pair_suggestions taxmin, bog'lashdan oldin juftni ko'rsat.
- Loyiha matni "muammo → qaror → natija" shaklida yoziladi (en: problem → decision → result): qanday muammo bor edi, nima qaror qilindi va nega, natija raqam yoki aniq fakt bilan.
- Avval ro'yxat va audit tool'lari, to'liq matn (get_post, get_project) faqat kerak bo'lganda - har bir o'qilgan bayt token.
- Sayt matnlarida uzun tire va o'rta tire ishlatilmaydi, faqat oddiy "-". Yozish tool'lari ularni o'zi almashtiradi va dashes_replaced deb xabar beradi.
- Har bir yozish amali avtomatik ravishda agent jurnaliga yoziladi va ops bot Ibrohimga xabar beradi. "Nima o'zgartirding?" yoki "qaytar" deyilsa, list_changes dan foydalan: qaytarish uchun from qiymatlarini update_post / update_project bilan qayta yoz.
- O'chirish tool'i ataylab yo'q. O'chirish kerak bo'lsa, admin panelda qilinadi.
- Moliya va o'sish ma'lumotlari Ibrohimning shaxsiy ma'lumoti: so'ralmasa xulosa ber, alohida yozuvlarni ro'yxat qilma.
- add_finance_entry va log_practice ga Ibrohimning o'z so'zlarini o'zgartirmay ber ("taksi 25k", "45 w essay") - bot bilan bir xil parser o'qiydi. Bir xabarda bir nechta yozuv bo'lsa, har biri alohida chaqiruv. Yozgandan keyin natijani qisqa ko'rsat (summa, kategoriya, sana).
- Ustuvorlik: dekabrgacha IELTS sprinti koddan oldin turadi. Haftalik xulosada o'sish bo'limini birinchi qo'y.`

var slugArgRe = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

func prompts(now func() time.Time) []mcp.Prompt {
	return []mcp.Prompt{
		{
			Name: "haftalik_xulosa", Title: "Haftalik xulosa",
			Description: "Oxirgi 7 kunlik holat: o'sish, tizim, xabarlar, kontent, moliya - va 3 ta keyingi qadam.",
			Render: func(map[string]string) (string, error) {
				today := now().Format("2006-01-02")
				from := now().AddDate(0, 0, -6).Format("2006-01-02")
				return fmt.Sprintf(`%s dan %s gacha bo'lgan hafta bo'yicha xulosa tayyorla. Hech narsani o'zgartirma.

1. growth_summary (from=%s, to=%s): sprint natijasi, daqiqalar, eng kam mashq qilingan tur.
2. system_overview: uptime, yangi yoki takrorlanayotgan xatolar, backup va deploy holati.
3. list_messages (unread_only=true): javob kutayotganlar.
4. content_audit: eng muhim 3 ta muammo.
5. finance_summary (from=%s, to=%s): jami xarajat, eng katta kategoriya, oldingi hafta bilan farq.

Har bo'lim 2-3 qator. Oxirida: shu hafta uchun 3 ta aniq keyingi qadam.`, from, today, from, today, from, today), nil
			},
		},
		{
			Name: "loyihani_qayta_yoz", Title: "Loyihani case study qilish",
			Description: "Bitta loyihani muammo → qaror → natija shakliga qayta yozish (uz va en).",
			Arguments:   []mcp.PromptArgument{{Name: "slug", Description: "loyiha slug'i", Required: true}},
			Render: func(a map[string]string) (string, error) {
				slug := strings.TrimSpace(a["slug"])
				if !slugArgRe.MatchString(slug) {
					return "", fmt.Errorf("slug %q is not a slug", slug)
				}
				return fmt.Sprintf(`"%s" loyihasini case study shakliga keltiramiz.

1. list_projects bilan uning uz va en versiyalarini top, get_project bilan matnini o'qi.
2. Matnda yo'q faktlarni (raqamlar, foydalanuvchilar soni, muddatlar) menga savol qilib ber - o'zing to'qima.
3. Har ikki til uchun yangi description (1-2 gap) va body taklif qil: ## Muammo / ## Qaror / ## Natija (en: Problem / Decision / Result).
4. Men tasdiqlagandan keyingina update_project bilan yoz. repo va url bo'sh bo'lsa, ularni ham so'ra.`, slug), nil
			},
		},
		{
			Name: "post_qoralama", Title: "Ikki tilli post qoralamasi",
			Description: "Mavzu bo'yicha uz va en qoralama post (nashr qilinmaydi).",
			Arguments:   []mcp.PromptArgument{{Name: "mavzu", Description: "post mavzusi", Required: true}},
			Render: func(a map[string]string) (string, error) {
				topic := strings.TrimSpace(a["mavzu"])
				if topic == "" {
					return "", fmt.Errorf("mavzu is empty")
				}
				return fmt.Sprintf(`Mavzu: %s

1. list_posts bilan bu mavzuda post bor-yo'qligini tekshir.
2. Avval reja ko'rsat: sarlavha, 4-6 bo'lim, har birida nima bo'ladi, slug va translation_key. Haqiqiy tajribamga tayan; bilmagan joyingni so'ra.
3. Men tasdiqlasam, uz va en versiyani create_post_draft bilan bir xil translation_key ostida yarat.
4. Nashr qilma.`, topic), nil
			},
		},
	}
}
