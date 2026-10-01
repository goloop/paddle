[![deps.dev](https://img.shields.io/badge/deps.dev-insights-4c8dbc)](https://deps.dev/go/github.com%2Fgoloop%2Fpaddle) [![Go Reference](https://img.shields.io/badge/godoc-reference-blue)](https://pkg.go.dev/github.com/goloop/paddle) [![License](https://img.shields.io/badge/license-MIT-brightgreen)](https://github.com/goloop/paddle/blob/master/LICENSE) [![Stay with Ukraine](https://img.shields.io/static/v1?label=Stay%20with&message=Ukraine%20♥&color=ffD700&labelColor=0057B8&style=flat)](https://u24.gov.ua/)

# paddle

`paddle` - Go-клієнт для Paddle Billing API і перевірка його вебхуків:
серверна частина продажів через Paddle як merchant of record - разові
покупки, довільні суми, підписки з будь-яким строком, місця (seats), зміна
плану, рахунки, повернення й підписані сповіщення. Лише стандартна
бібліотека.

Англійська версія: **[README.md](README.md)**.

## Яку проблему розв'язує

Paddle продає, списує гроші, займається ПДВ і податками з продажів і
повідомляє ваш сервер, що сталося. Частина сервера невелика, але кожен її
шматок має пастку:

- **Сума має приходити з сервера.** Ціну, задану в браузері, у браузері й
  змінять. Транзакції створюються тут, з цінами з каталогу чи поза ним, а
  checkout лише їх відкриває.
- **Вебхуки треба перевіряти на сирих байтах і вчасно.** Декодоване й знову
  закодоване тіло вже не проходить перевірку; стара доставка, відтворена
  пізніше, не має пройти; секрет під час ротації має працювати.
- **POST, що впав, міг виконатися.** Повтор транзакції чи повернення після
  5xx може створити другу; ключа ідемпотентності API не приймає. Клієнт
  повторює лише те, що точно не виконалося.
- **Sandbox-ключ не має потрапити в продакшн.** Хост обирається за самим
  ключем.
- **Chargeback - не оновлення.** Повернення, схвалене пізніше, приходить як
  `adjustment.updated`; chargeback і повернення, схвалене одразу, - лише як
  `adjustment.created`. Обробник, що слухає одну подію, пропускає гроші, які
  повертаються.
- **Загублений вебхук - загублені гроші.** Paddle припиняє повтори через три
  дні; `ListTransactions` і `ListAdjustments` дають періодичній задачі знайти
  те, що не прийшло.

Пакет робить ці речі правильно, а решта - тонке типізоване
відображення API.

## Можливості

- Транзакції: ціни з каталогу, ціни поза каталогом для сум, визначених на
  сервері, custom data, що повертається в кожній події, рахунки з ручною
  оплатою.
- Підписки: отримати, скасувати в кінці періоду чи одразу, відкликати
  заплановану зміну, призупинити, відновити, змінити план чи кількість місць
  з перерахунком.
- Повернення (повні чи часткові) і chargeback через adjustments.
- Клієнти (`EnsureCustomer` переживає гонку і повертає архівного клієнта) і
  сесії клієнтського порталу.
- Вебхуки: перевірка підпису з ротацією й вікном від повторів, типізовані
  події.
- Звірка: `ListTransactions` і `ListAdjustments` посторінково.
- Типізована `*APIError`; повтори, що не можуть списати двічі; редиректи не
  виконуються, id перевіряються за дозвільним списком, ключі не друкуються.
- Нуль залежностей.

## Встановлення

```bash
go get github.com/goloop/paddle
```

## Швидкий старт

```go
c := paddle.New(os.Getenv("PADDLE_API_KEY")) // sandbox-ключ іде в sandbox

tx, err := c.CreateTransaction(ctx, &paddle.CreateTransactionRequest{
	Items: []paddle.TransactionItemRequest{{Price: &paddle.NonCatalogPrice{
		Description: "1000 credits",
		ProductID:   "pro_credits",
		UnitPrice:   paddle.Money{Amount: "1000", CurrencyCode: "USD"},
	}}},
	CustomData: map[string]any{"purchase": purchaseID},
})
// відкрити checkout у браузері: Paddle.Checkout.open({transactionId: tx.ID})
```

## Моделі продажу

| Модель | Приклад | Як |
|---|---|---|
| **Разово, фіксована ціна** | ліцензія, фіксований пакет | `PriceID` з каталогу |
| **Разово, сума з сервера** | пакети кредитів, індивідуальні замовлення | `NonCatalogPrice` під одним продуктом |
| **Підписка** | $15 на місяць, $60 на рік | регулярна ціна з каталогу (`billing_cycle`) |
| **Довші строки** | $100 за два роки | `billing_cycle` `year` x `2` - спершу перевірте в sandbox |
| **Регулярна сума з сервера** | узгоджений план | `NonCatalogPrice` з `BillingCycle` |
| **Пробний період** | 14 днів безкоштовно | `trial_period` у ціні |
| **Місця (seats)** | $8 за учасника на місяць | `Quantity`; `ChangeSubscriptionItems`, щоб додати чи прибрати |
| **Зміна плану** | місяць на рік, Personal на Team | `ChangeSubscriptionItems` з режимом перерахунку |
| **Скасування, пауза, передумав** | скасувати в кінці періоду | `CancelSubscription`, `RemoveScheduledChange`, `PauseSubscription` |
| **Рахунки для компаній** | оплата за 30 днів, номер замовлення | `CollectionMode: "manual"` + `BillingDetails` |
| **Повернення й chargeback** | 14 днів на відмову | `RefundTransaction`, вебхуки adjustments |

Кредити, ліміти й речі, оплачені з гаманця, - не поняття Paddle: продавайте
тут пакети, а баланс тримайте в
[goloop/ledger](https://github.com/goloop/ledger). Довідник показує кожну
модель у коді: [Моделі продажу](DOC.UK.md#моделі-продажу).

## Реальний приклад

```go
wh := paddle.NewWebhook(os.Getenv("PADDLE_WEBHOOK_SECRET"))

http.HandleFunc("POST /webhooks/paddle", func(w http.ResponseWriter, r *http.Request) {
	ev, err := wh.ParseRequest(r)
	if err != nil {
		http.Error(w, "bad signature", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	// Таблиця подій - скорочення, а не захист: apply ідемпотентний сам
	// (записи з ключами), а подію позначено виконаною лише після застосування,
	// тож невдачу тут Paddle повторить, і вона не загубиться.
	done, err := events.Done(ctx, ev.ID) // ваша таблиця
	if err != nil {
		http.Error(w, "retry", http.StatusInternalServerError)
		return
	}
	if done {
		w.WriteHeader(http.StatusOK)
		return
	}
	if err := apply(ctx, ev); err != nil {
		http.Error(w, "retry", http.StatusInternalServerError) // Paddle надішле ще раз
		return
	}
	if err := events.MarkDone(ctx, ev.ID); err != nil {
		http.Error(w, "retry", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
})

func apply(ctx context.Context, ev *paddle.Event) error {
	switch ev.Type {
	case paddle.EventTransactionCompleted:
		tx, err := ev.Transaction()
		if err != nil {
			return err
		}
		return purchases.Paid(ctx, tx) // звірити суму й валюту, потім нарахувати, ключ tx.ID

	case paddle.EventSubscriptionUpdated, paddle.EventSubscriptionCanceled:
		sub, err := ev.Subscription()
		if err != nil {
			return err
		}
		return entitlements.Sync(ctx, sub, ev.OccurredAt) // ігнорує подію, старішу за стан

	// Повернення, схвалене пізніше, приходить як adjustment.updated; chargeback
	// і повернення, схвалене одразу, - лише як adjustment.created.
	case paddle.EventAdjustmentCreated, paddle.EventAdjustmentUpdated:
		adj, err := ev.Adjustment()
		if err != nil {
			return err
		}
		return purchases.Adjusted(ctx, adj) // діє лише на approved, ключ adj.ID
	}
	return nil
}
```

## API коротко

| Виклик | Ендпоінт |
|---|---|
| `CreateTransaction`, `GetTransaction`, `ListTransactions` | `/transactions` |
| `GetSubscription`, `CancelSubscription`, `PauseSubscription`, `ResumeSubscription` | `/subscriptions/{id}[/cancel,/pause,/resume]` |
| `ChangeSubscriptionItems`, `RemoveScheduledChange` | `PATCH /subscriptions/{id}` |
| `CreateAdjustment`, `RefundTransaction`, `ListAdjustments` | `/adjustments` |
| `CreateCustomer`, `GetCustomer`, `CustomerByEmail`, `EnsureCustomer` | `/customers` |
| `CreatePortalSession` | `/customers/{id}/portal-sessions` |
| `NewWebhook`, `Webhook.ParseRequest`, `Sign` | сповіщення |

## Документація

- Повний довідник і моделі продажу: [DOC.UK.md](DOC.UK.md) · [DOC.md](DOC.md)
- API пакета: [pkg.go.dev/github.com/goloop/paddle](https://pkg.go.dev/github.com/goloop/paddle)
- Зміни між версіями: [CHANGELOG.md](CHANGELOG.md)
- Гаманці кредитів поверх: [goloop/ledger](https://github.com/goloop/ledger)

## Участь

Внески вітаються. Перед pull request запустіть `go test -race ./...`,
`go vet ./...` і `gofmt -l .`.

## Ліцензія

`paddle` поширюється за ліцензією MIT. Див. [LICENSE](LICENSE).
