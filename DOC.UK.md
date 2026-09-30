# paddle - довідник

Повний довідник пакета `paddle`: клієнт, транзакції, підписки, adjustments,
клієнти, вебхуки, повтори й помилки.

Англійська версія: **[DOC.md](DOC.md)**.

## Зміст

- [Модель](#модель)
- [Створення клієнта](#створення-клієнта)
- [Транзакції](#транзакції)
- [Підписки](#підписки)
- [Повернення й adjustments](#повернення-й-adjustments)
- [Клієнти й портал](#клієнти-й-портал)
- [Моделі продажу](#моделі-продажу)
- [Вебхуки](#вебхуки)
- [Повтори](#повтори)
- [Помилки](#помилки)
- [Суми й час](#суми-й-час)
- [Межі пакета](#межі-пакета)

## Модель

Paddle - merchant of record: він продає, списує гроші, займається податками
й поверненнями та повідомляє ваш сервер про події через підписані вебхуки.
Частина сервера невелика - створити транзакцію для checkout, відреагувати на
події, іноді скасувати підписку чи попросити повернення - і саме це покриває
пакет.

Джерело правди про гроші - провайдер. Джерело правди про те, чим клієнт може
користуватися, - ваша база. Вебхуки - єдина дорога між ними, тому їх
перевіряють раніше за все інше й дедуплікують за id події.

## Створення клієнта

```go
c := paddle.New(os.Getenv("PADDLE_API_KEY"))

c = paddle.New(apiKey,
	paddle.WithTimeout(10*time.Second),
	paddle.WithMaxRetries(3),
)
```

Хост визначається ключем: sandbox-ключ (`pdl_sdbx_...`) іде на
`https://sandbox-api.paddle.com`, будь-який інший - на
`https://api.paddle.com`. `WithSandbox(bool)` і `WithBaseURL(url)`
перевизначають це; `Sandbox()` каже, куди звертається клієнт. Кожен запит
надсилає `Paddle-Version: 1` (`WithVersion` змінює).

| Опція | За замовчуванням |
|---|---|
| `WithTimeout` | 30s (лише для HTTP-клієнта за замовчуванням) |
| `WithHTTPClient` | новий `http.Client` |
| `WithMaxRetries` | 2 |
| `WithHeader` | немає |

## Транзакції

Транзакція - це те, що клієнт оплачує в checkout. Створюйте її на сервері,
щоб суму не можна було змінити в браузері, і відкривайте checkout за її id.

```go
tx, err := c.CreateTransaction(ctx, &paddle.CreateTransactionRequest{
	Items: []paddle.TransactionItemRequest{
		{PriceID: "pri_monthly"}, // ціна з каталогу, кількість 1
	},
	CustomerID: customer.ID, // необов'язково
	CustomData: map[string]any{"purchase": purchaseID},
})
```

**Ціна поза каталогом** списує суму, визначену на сервері, під одним
продуктом каталогу - пакет кредитів, індивідуальне замовлення - без окремої
ціни в каталозі на кожну суму. `Description` і `ProductID` обов'язкові:

```go
{Price: &paddle.NonCatalogPrice{
	Description: "1000 credits",
	ProductID:   "pro_credits",
	UnitPrice:   paddle.Money{Amount: "1000", CurrencyCode: "USD"},
	TaxMode:     paddle.TaxModeAccountSetting, // за замовчуванням
}}
```

`BillingCycle` у ціні поза каталогом робить її регулярною.

API не приймає email у транзакції. Коли адреса відома, спершу знайдіть чи
створіть клієнта (`EnsureCustomer`) і передайте `CustomerID`; без нього
checkout сам запитає email.

Кладіть id вашого рядка покупки в `CustomData`: він повертається в кожній
події про транзакцію, тож вебхук можна зіставити, навіть якщо зберегти id
транзакції не вдалося.

`GetTransaction(ctx, id)` читає транзакцію - для звірки.

## Підписки

```go
sub, err := c.GetSubscription(ctx, "sub_01...")

sub, err = c.CancelSubscription(ctx, id, paddle.EffectiveNextBillingPeriod) // за замовчуванням
sub, err = c.CancelSubscription(ctx, id, paddle.EffectiveImmediately)

sub, err = c.RemoveScheduledChange(ctx, id) // клієнт передумав

sub, err = c.PauseSubscription(ctx, id, &paddle.PauseRequest{ResumeAt: &date})
sub, err = c.ResumeSubscription(ctx, id, nil)
```

Скасування в кінці періоду лишає підписку `active` зі
`ScheduledChange{Action: "cancel"}`, і `RemoveScheduledChange` його
відкликає. Негайне скасування остаточне: скасовану підписку не відновити.

`Subscription.CurrentBillingPeriod` - період, за який клієнт заплатив;
давайте права плану до його `EndsAt`. `ManagementURLs` містить посилання
Paddle для зміни способу оплати й скасування.

## Повернення й adjustments

```go
adj, err := c.RefundTransaction(ctx, "txn_01...", "Withdrawal within 14 days")
```

Повернення потребує транзакції в статусі `completed` і починається як
`AdjustmentPendingApproval`; вебхук `adjustment.updated` повідомляє
`approved` чи `rejected`. Часткове повернення перелічує позиції:

```go
adj, err := c.CreateAdjustment(ctx, &paddle.CreateAdjustmentRequest{
	Action:        paddle.ActionRefund,
	TransactionID: "txn_01...",
	Reason:        "one of three items was not delivered",
	Type:          "partial",
	Items:         []paddle.AdjustmentItem{{ItemID: "txnitm_01...", Type: "full"}},
})
```

Chargeback теж приходить як adjustment, який створює Paddle:
`ActionChargeback`, `ActionChargebackReverse`, `ActionChargebackWarning`.
Схвалений chargeback обробляйте як повернення: заберіть те, що купили за ці
гроші.

## Клієнти й портал

```go
cu, err := c.EnsureCustomer(ctx, "ann@example.com", "Ann") // знайти або створити
cu, err = c.CustomerByEmail(ctx, "ann@example.com")        // ErrNotFound, якщо немає
cu, err = c.GetCustomer(ctx, "ctm_01...")

ps, err := c.CreatePortalSession(ctx, cu.ID, sub.ID)
// перенаправити на ps.URLs.General.Overview чи ps.URLs.Subscriptions[0].CancelSubscription
```

`CreateCustomer` повертає код `customer_already_exists`, коли адреса вже
належить клієнту; `EnsureCustomer` обробляє це, як і два запити, що
одночасно створюють того самого клієнта.

Посилання порталу тимчасові: створюйте сесію, коли клієнт просить, і ніколи
не зберігайте посилання.

## Моделі продажу

Каталог Paddle має **продукти** (що ви продаєте) і **ціни** (скільки й як
часто). Транзакція виставляє ціни до оплати; підписка - це те, чим стає
транзакція з регулярною ціною після оплати. Кожна модель нижче - поєднання
цих двох.

### Разова покупка за фіксованою ціною

Ліцензія, фіксований пакет, доповнення. Створіть ціну в каталозі й
виставляйте її за id:

```go
tx, err := c.CreateTransaction(ctx, &paddle.CreateTransactionRequest{
	Items:      []paddle.TransactionItemRequest{{PriceID: "pri_maker_licence"}},
	CustomData: map[string]any{"purchase": purchaseID},
})
```

На `transaction.completed` видайте право (рядок ліцензії зі строком
оновлень) у таблиці прав.

### Разова покупка, сума з сервера

Пакети кредитів з довільною сумою, замовлення з ціною за формулою,
поповнення. Один продукт у каталозі, ціна поза каталогом на кожну
транзакцію:

```go
tx, err := c.CreateTransaction(ctx, &paddle.CreateTransactionRequest{
	Items: []paddle.TransactionItemRequest{{Price: &paddle.NonCatalogPrice{
		Description: fmt.Sprintf("%d credits", pack.Credits),
		ProductID:   "pro_credits",
		UnitPrice:   paddle.Money{Amount: strconv.FormatInt(pack.Cents, 10), CurrencyCode: "EUR"},
	}}},
	CustomData: map[string]any{"purchase": purchaseID},
})
```

Коли прийде вебхук, перед нарахуванням звірте `tx.Details.Totals.Subtotal` і
валюту з рядком покупки: транзакцію можуть змінити в Paddle вже після того,
як ви її створили.

### Підписки та строки

Регулярна ціна має `billing_cycle`: `month` x `1`, `year` x `1`. Створіть
окрему ціну на кожен строк під тим самим продуктом і продавайте строк, який
обрав клієнт:

| Строк | Ціна | За місяць |
|---|---|---|
| місяць | `pri_pro_month`, $15, month x 1 | $15 |
| рік | `pri_pro_year`, $60, year x 1 | $5 |
| два роки | `pri_pro_2y`, $100, year x 2 | $4.17 |

```go
tx, err := c.CreateTransaction(ctx, &paddle.CreateTransactionRequest{
	Items:      []paddle.TransactionItemRequest{{PriceID: "pri_pro_year"}},
	CustomerID: cu.ID,
	CustomData: map[string]any{"account": accountID},
})
```

Після оплати транзакція створює підписку, і приходять `subscription.created`
/ `subscription.activated`. Давайте права плану до
`CurrentBillingPeriod.EndsAt` і пересувайте цю дату на кожному
`subscription.updated`. Частоту більше одиниці схема приймає; перевірте
багаторічний цикл у sandbox, перш ніж його рекламувати.

Регулярна сума, визначена на сервері, - узгоджений план - це
`NonCatalogPrice` із заданим `BillingCycle`.

### Пробний період

Задайте `trial_period` у ціні з каталогу (або `TrialPeriod` у ціні поза
каталогом). Підписка починається як `trialing` і виставляє рахунок після
пробного періоду; скасування під час нього нічого не коштує.

### Місця (seats)

Ціна за місце з `Quantity`:

```go
// Десять місць на старті.
Items: []paddle.TransactionItemRequest{{PriceID: "pri_team_seat_month", Quantity: 10}}

// Ще двоє учасників пізніше: різницю виставити зараз.
sub, err := c.ChangeSubscriptionItems(ctx, sub.ID,
	[]paddle.SubscriptionItemRequest{{PriceID: "pri_team_seat_month", Quantity: 12}},
	paddle.ProratedImmediately)
```

Передані позиції - це весь новий набір.

### Зміна плану

Місяць на рік, Personal на Team, підвищення чи зниження - замініть ціну:

```go
sub, err := c.ChangeSubscriptionItems(ctx, sub.ID,
	[]paddle.SubscriptionItemRequest{{PriceID: "pri_pro_year"}},
	paddle.ProratedImmediately)
```

| Режим перерахунку | Дія |
|---|---|
| `ProratedImmediately` | списати чи повернути різницю зараз (підвищення) |
| `ProratedNextBillingPeriod` | додати різницю до наступного рахунку |
| `FullImmediately` | списати нову ціну повністю зараз |
| `FullNextBillingPeriod` | перейти з наступного продовження, без перерахунку (зниження) |
| `DoNotBill` | змінити без рахунку (жест підтримки) |

### Скасування, пауза, «я передумав»

```go
sub, err := c.CancelSubscription(ctx, id, paddle.EffectiveNextBillingPeriod)
sub, err = c.RemoveScheduledChange(ctx, id) // «я передумав»
sub, err = c.PauseSubscription(ctx, id, &paddle.PauseRequest{ResumeAt: &resume})
```

Скасування в кінці періоду зберігає права до `EndsAt`; тоді підписка стає
`canceled` і приходить `subscription.canceled`. Для кнопки в інтерфейсі
`CreatePortalSession` дає власні сторінки Paddle для скасування й зміни
картки.

### Рахунки для компаній

Компанія, що платить банківським переказом з відтермінуванням:

```go
tx, err := c.CreateTransaction(ctx, &paddle.CreateTransactionRequest{
	Items:          []paddle.TransactionItemRequest{{PriceID: "pri_team_seat_year", Quantity: 25}},
	CustomerID:     cu.ID, // клієнт має мати ім'я
	AddressID:      addressID,
	BusinessID:     businessID,
	CollectionMode: "manual",
	BillingDetails: &paddle.BillingDetails{
		EnableCheckout:      true, // можуть заплатити й карткою
		PaymentTerms:        paddle.Duration{Interval: "day", Frequency: 30},
		PurchaseOrderNumber: "PO-2026-114",
	},
})
```

### Повернення, відмова, chargeback

```go
adj, err := c.RefundTransaction(ctx, tx.ID, "Withdrawal within 14 days")
```

Забирайте те, що куплено за ці гроші, лише коли `adjustment.updated` каже
`approved`, з ключем - id adjustment. Chargeback приходить так само, його
створює Paddle з `ActionChargeback`.

### Кредити, ліміти, використання

Paddle не тримає баланс кредитів і не рахує використання. Продавайте пакети
кредитів як разові покупки (вище), баланс і ліміти планів тримайте в
[goloop/ledger](https://github.com/goloop/ledger), а щомісячний ліміт
підписки нараховуйте з її вебхука. Разом це покриває оплату за дію, окремі
платні речі, плани з лімітом і оплату понад ліміт.

## Вебхуки

```go
wh := paddle.NewWebhook(secret)
ev, err := wh.ParseRequest(r)
```

Перевірка:

- Заголовок `Paddle-Signature` має вигляд `ts=<unix>;h1=<hex>`, де `h1` -
  HMAC-SHA256 від `<ts>:<сире тіло>` під секретом призначення.
- Тіло має бути байтами в тому вигляді, в якому прийшло. Декодоване й знову
  закодоване тіло не проходить перевірку.
- Поки Paddle ротує секрет, він надсилає кілька значень `h1`; достатньо
  одного збігу. `WithPreviousSecret` приймає старий секрет з вашого боку.
- Час підпису має бути в межах `DefaultTolerance` (5 секунд, як документує
  Paddle) від локального годинника, інакше доставка завершується
  `ErrSignatureExpired`. Тримайте годинник сервера синхронізованим;
  `WithTolerance` розширює вікно.
- Порожній секрет нічого не підтверджує, тож неправильно налаштований сервіс
  відхиляє кожну доставку, а не приймає підробки.

`ErrSignatureExpired` відповідає й `ErrSignature`, тож одна перевірка покриває
обидва випадки.

Обробка:

- Дедуплікуйте за `ev.ID`: Paddle доставляє щонайменше раз.
- Відповідайте 2xx протягом п'яти секунд; повільна відповідь вважається
  невдачею, і доставка повториться. Збережіть подію, відповідайте, потім
  обробляйте.
- Події можуть приходити не по порядку. Використовуйте `ev.OccurredAt` чи
  власний `UpdatedAt` сутності, щоб ігнорувати подію, старшу за вже
  застосований стан.

Декодування: `ev.Transaction()`, `ev.Subscription()`, `ev.Adjustment()`,
`ev.Customer()` або `ev.Decode(&v)` для решти. Константи `Event*` називають
поширені типи; будь-який інший тип теж розбирається.

`Sign(secret, body, at)` будує коректний заголовок - для тестів вашого
обробника.

## Повтори

| Невдача | GET | POST, PATCH |
|---|---|---|
| 429 | повтор після `Retry-After` | повтор після `Retry-After` |
| 5xx | повтор з паузою | повертається |
| мережева помилка | повтор з паузою | повертається |
| інші 4xx | повертається | повертається |

429 означає, що запит відхилили до виконання, тож повторювати його безпечно
для будь-якого методу. POST, що впав з 5xx чи обірваним з'єднанням, міг
виконатися, а API не приймає ключа ідемпотентності: повтор міг би створити
другу транзакцію чи друге повернення. Помилка повертається, і рішення
ухвалює той, хто викликає, - зазвичай спершу перечитавши стан
(`GetTransaction`).

`Retry-After` обмежено 60 секундами; пауза - 0.5s, 1s, 2s ... з
розкидом. Скасований контекст зупиняє очікування.

## Помилки

Відповідь з помилкою - це `*APIError`:

```go
var ae *paddle.APIError
if errors.As(err, &ae) {
	log.Printf("%d %s: %s (request %s)", ae.Status, ae.Code, ae.Detail, ae.RequestID)
	for _, f := range ae.Errors {
		log.Printf("  %s: %s", f.Field, f.Message)
	}
}
if errors.Is(err, paddle.ErrNotFound) { ... } // будь-який 404
```

`ErrInvalid` означає запит, відхилений до надсилання: бракує поля, id змінив
би шлях URL.

## Суми й час

Суми - рядки в найменшій одиниці валюти, точно як їх надсилає Paddle:
`"1000"` - це $10.00. Для арифметики конвертуйте через `strconv.ParseInt`.
Час - `time.Time`, необов'язковий - `*time.Time`.

## Межі пакета

Серверні виклики, потрібні продукту, а не весь API: без керування каталогом,
звітів, знижок чи адрес. Спільний шлях `do` приймає будь-який ендпоінт, тож
відсутній виклик - невелике доповнення, а не новий клієнт.

Пакет залежить лише від стандартної бібліотеки.
