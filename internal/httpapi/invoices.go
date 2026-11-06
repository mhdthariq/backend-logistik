package httpapi

import (
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgconn"
)

func (b *business) registerInvoices(app *fiber.App) {
	app.Get("/api/v1/invoices", b.handle("invoice.view", true, false, b.invoices))
	app.Post("/api/v1/invoices", b.handle("invoice.create", true, true, b.createInvoice))
	app.Get("/api/v1/invoices/:id", b.handle("invoice.view", true, false, b.invoiceDetail))
	app.Put("/api/v1/invoices/:id", b.handle("invoice.update", true, true, b.updateInvoice))
	app.Delete("/api/v1/invoices/:id", b.handle("invoice.update", true, true, b.deleteInvoice))
	app.Post("/api/v1/invoices/:id/lines", b.handle("invoice.update", true, true, b.addInvoiceLine))
	app.Delete("/api/v1/invoice-lines/:id", b.handle("invoice.update", true, true, b.deleteInvoiceLine))
	app.Post("/api/v1/invoices/:id/send", b.handle("invoice.send", true, true, b.sendInvoice))
	app.Post("/api/v1/invoices/:id/print-log", b.handle("invoice.view", true, false, b.printInvoice))
	app.Post("/api/v1/invoices/:id/settlements", b.handle("payment.verify", true, true, b.mutateInvoiceSettlement))
	app.Put("/api/v1/invoices/:id/settlements/:settlementId", b.handle("invoice.settlement.update", true, true, b.mutateInvoiceSettlement))
	app.Delete("/api/v1/invoices/:id/settlements/:settlementId", b.handle("invoice.settlement.delete", true, true, b.mutateInvoiceSettlement))
}

const invoiceSelect = `SELECT to_jsonb(i)||jsonb_build_object('customer',to_jsonb(c),
 'lines',COALESCE((SELECT jsonb_agg(to_jsonb(l)||jsonb_build_object('shipment',CASE WHEN s.id IS NULL THEN NULL ELSE jsonb_build_object('id',s.id,'masterCode',s."masterCode") END) ORDER BY l.id) FROM "InvoiceLine" l LEFT JOIN "MasterShipment" s ON s.id=l."shipmentId" WHERE l."invoiceId"=i.id),'[]'::jsonb),
 'settlements',COALESCE((SELECT jsonb_agg(to_jsonb(s) ORDER BY s."settledAt" DESC,s.id DESC) FROM "InvoiceSettlement" s WHERE s."invoiceId"=i.id),'[]'::jsonb),
 'commission',(SELECT to_jsonb(m)||jsonb_build_object('partner',` + partnerJoin + `) FROM "MarketingCommission" m JOIN "Partner" p ON p.id=m."partnerId" JOIN "User" u ON u.id=p."userId" WHERE m."invoiceId"=i.id)) FROM "Invoice" i JOIN "Customer" c ON c.id=i."customerId" `

func invoiceTotals(i map[string]any) {
	total := -number(i["discount"]) + number(i["rounding"])
	paid, pph := 0.0, 0.0
	for _, l := range objects(i["lines"]) {
		total += number(l["quantity"]) * number(l["unitPrice"])
	}
	for _, s := range objects(i["settlements"]) {
		paid += number(s["amount"])
		pph += number(s["pph"])
	}
	i["totalAmount"], i["settledAmount"], i["remainingAmount"], i["pphAmount"] = total, paid, total-paid, pph
}
func (r *request) loadInvoice(id int, lock bool) (map[string]any, error) {
	if lock {
		row, err := r.one(`SELECT jsonb_build_object('id',id) FROM "Invoice" WHERE id=$1 FOR UPDATE`, id)
		if err != nil {
			return nil, err
		}
		if row == nil {
			return nil, problem(404, "Invoice tidak ditemukan.")
		}
	}
	i, err := r.one(invoiceSelect+`WHERE i.id=$1`, id)
	if err != nil {
		return nil, err
	}
	if i == nil {
		return nil, problem(404, "Invoice tidak ditemukan.")
	}
	invoiceTotals(i)
	return i, nil
}
func (b *business) invoiceDetail(r *request) (any, error) {
	return r.loadInvoice(idValue(r.c.Params("id")), false)
}
func (b *business) invoices(r *request) (any, error) {
	rows, err := r.many(invoiceSelect+`WHERE ($1='' OR i.status=$1) AND ($2='' OR strpos(lower(i."invoiceNumber"),lower($2))>0 OR strpos(lower(c.name),lower($2))>0 OR strpos(lower(c."companyName"),lower($2))>0) ORDER BY i."createdAt" DESC,i.id DESC`, r.c.Query("status"), r.c.Query("search"))
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, i := range rows {
		invoiceTotals(i)
		c := object(i["customer"])
		m := object(i["commission"])
		var commission any
		if i["commission"] != nil {
			commission = map[string]any{"status": m["status"], "partnerName": object(object(m["partner"])["user"])["name"], "commissionAmount": m["commissionAmount"], "partnerPercent": m["partnerPercent"]}
		}
		var last any
		for _, s := range objects(i["settlements"]) {
			if last == nil || textValue(s["settledAt"]) > textValue(last) {
				last = s["settledAt"]
			}
		}
		due, _ := dateValue(i["dueDate"], false).(time.Time)
		row := map[string]any{"customerName": fallback(c["companyName"], c["name"]), "customerCompanyName": c["companyName"], "customerCode": c["code"], "customerType": c["type"], "linesCount": len(objects(i["lines"])), "lastPaidAt": last, "isOverdue": i["status"] == "SENT" && !due.IsZero() && due.Before(time.Now()), "commission": commission}
		for _, k := range []string{"id", "invoiceNumber", "customerId", "status", "discount", "rounding", "issueDate", "dueDate", "notes", "totalAmount", "settledAmount", "pphAmount", "remainingAmount", "createdAt"} {
			row[k] = i[k]
		}
		out = append(out, row)
	}
	return out, nil
}
func (r *request) invoiceBank(value any) (map[string]any, error) {
	data := map[string]any{"bankAccountId": nil, "paymentBankName": nil, "paymentAccountNumber": nil, "paymentAccountName": nil}
	if value == nil || value == "" {
		return data, nil
	}
	id := idValue(value)
	if id == 0 {
		return nil, problem(422, "Rekening tidak valid.")
	}
	a, err := r.one(`SELECT to_jsonb(a) FROM "BankAccount" a WHERE id=$1 AND "isActive"`, id)
	if err != nil {
		return nil, err
	}
	if a == nil {
		return nil, problem(422, "Rekening tidak ditemukan atau sudah nonaktif.")
	}
	data["bankAccountId"], data["paymentBankName"], data["paymentAccountNumber"], data["paymentAccountName"] = id, a["bankName"], a["accountNumber"], a["accountName"]
	return data, nil
}
func invoiceLines(raw any) ([]map[string]any, error) {
	out := []map[string]any{}
	for _, l := range objects(raw) {
		desc := textValue(l["description"])
		if desc == "" {
			continue
		}
		qty, price := 1.0, 0.0
		if n := numeric(l["quantity"]); n != nil {
			qty = number(n)
		}
		if n := numeric(l["unitPrice"]); n != nil {
			price = number(n)
		}
		if qty <= 0 || price < 0 || !finite(qty*price) {
			return nil, problem(422, "Jumlah dan harga baris invoice tidak valid.")
		}
		var shipment any
		if l["shipmentId"] != nil && l["shipmentId"] != "" {
			id := idValue(l["shipmentId"])
			if id == 0 {
				return nil, problem(422, "Shipment tidak valid.")
			}
			shipment = id
		}
		out = append(out, map[string]any{"description": desc, "quantity": qty, "unitPrice": price, "shipmentId": shipment})
	}
	if len(out) == 0 {
		return nil, fieldProblem("Minimal satu baris item wajib diisi.", "lines", "Minimal satu baris item.")
	}
	return out, nil
}
func (r *request) validateInvoiceLines(lines []map[string]any, customer any, invoice int) (int, error) {
	partner := 0
	seen := map[int]bool{}
	for _, l := range lines {
		id := idValue(l["shipmentId"])
		if id == 0 {
			continue
		}
		if seen[id] {
			return 0, problem(422, "Shipment hanya boleh muncul satu kali dalam invoice.")
		}
		seen[id] = true
		s, err := r.one(`SELECT to_jsonb(s) FROM "MasterShipment" s WHERE id=$1 FOR UPDATE`, id)
		if err != nil {
			return 0, err
		}
		if s == nil {
			return 0, problem(404, "Shipment tidak ditemukan.")
		}
		if idValue(s["customerId"]) != idValue(customer) {
			return 0, problem(422, "Shipment tidak cocok dengan customer invoice.")
		}
		arrived, err := r.one(`SELECT jsonb_build_object('id',t.id) FROM "TransportShipment" ts JOIN "Transport" t ON t.id=ts."transportId" WHERE ts."shipmentId"=$1 AND t.status IN ('ARRIVED','COMPLETED') LIMIT 1`, id)
		if err != nil {
			return 0, err
		}
		if s["status"] == "CANCELLED" || arrived == nil {
			return 0, problem(422, "Shipment hanya dapat ditagihkan setelah transport berstatus Tiba.")
		}
		existing, err := r.one(`SELECT jsonb_build_object('id',id) FROM "InvoiceLine" WHERE "shipmentId"=$1 AND "invoiceId"<>$2`, id, invoice)
		if err != nil {
			return 0, err
		}
		if existing != nil {
			return 0, problem(422, "Shipment sudah termasuk dalam invoice lain.")
		}
		if p := idValue(s["createdByPartnerId"]); p != 0 {
			if partner != 0 && partner != p {
				return 0, problem(422, "Shipment dari beberapa Marketing partner tidak bisa digabung.")
			}
			partner = p
		}
	}
	return partner, nil
}
func (r *request) insertInvoiceLine(invoice any, l map[string]any) (map[string]any, error) {
	row, err := r.one(`INSERT INTO "InvoiceLine" AS l ("invoiceId",description,quantity,"unitPrice","shipmentId") VALUES ($1,$2,$3,$4,$5) RETURNING to_jsonb(l)`, invoice, l["description"], l["quantity"], l["unitPrice"], l["shipmentId"])
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return nil, problem(422, "Shipment sudah termasuk dalam invoice lain.")
	}
	return row, err
}
func (r *request) syncInvoiceCommission(id int, partner int) error {
	i, err := r.loadInvoice(id, false)
	if err != nil {
		return err
	}
	total := number(i["totalAmount"])
	if !finite(total) || total < 0 {
		return problem(422, "Total invoice tidak boleh negatif.")
	}
	m := object(i["commission"])
	if partner == 0 {
		return r.exec(`DELETE FROM "MarketingCommission" WHERE "invoiceId"=$1 AND status='PENDING'`, id)
	}
	p, err := r.partnerBrief(partner)
	if err != nil {
		return err
	}
	if p == nil {
		return problem(422, "Partner tidak ditemukan.")
	}
	companyRate, partnerRate := p["companyPercent"], p["partnerPercent"]
	if i["commission"] != nil && idValue(m["partnerId"]) == partner {
		// Editing a draft changes its value, not its recorded profit-share rates.
		companyRate, partnerRate = m["companyPercent"], m["partnerPercent"]
	}
	amount := money(total * number(partnerRate) / 100)
	if !finite(amount) {
		return problem(422, "Nilai komisi melebihi batas.")
	}
	data := map[string]any{"partnerId": partner, "invoiceId": id, "invoiceAmount": total, "companyPercent": companyRate, "partnerPercent": partnerRate, "commissionAmount": amount}
	if i["commission"] != nil {
		_, err = r.updateRow("MarketingCommission", idValue(m["id"]), data)
		return err
	}
	code, err := r.nextCode("MarketingCommission", "commissionCode", "COM-", 731043)
	if err != nil {
		return err
	}
	data["commissionCode"] = code
	data["status"] = "PENDING"
	_, err = r.insertRow("MarketingCommission", data)
	return err
}
func (b *business) createInvoice(r *request) (any, error) {
	customer := idValue(r.body["customerId"])
	c, err := r.one(`SELECT to_jsonb(c) FROM "Customer" c WHERE id=$1`, customer)
	if err != nil {
		return nil, err
	}
	if c == nil || c["type"] != "b2b" {
		return nil, fieldProblem("Invoice hanya untuk customer B2B.", "customerId", "Customer B2B wajib dipilih.")
	}
	data, err := r.invoiceBank(r.body["bankAccountId"])
	if err != nil {
		return nil, err
	}
	lines, err := invoiceLines(r.body["lines"])
	if err != nil {
		return nil, err
	}
	partner, err := r.validateInvoiceLines(lines, customer, 0)
	if err != nil {
		return nil, err
	}
	discount, rounding := number(r.body["discount"]), number(r.body["rounding"])
	if discount < 0 {
		return nil, problem(422, "Diskon tidak boleh negatif.")
	}
	code, err := r.nextCode("Invoice", "invoiceNumber", "INV-2026-", 731042)
	if err != nil {
		return nil, err
	}
	data["invoiceNumber"], data["customerId"], data["status"], data["discount"], data["rounding"], data["issueDate"], data["dueDate"], data["notes"] = code, customer, "DRAFT", discount, rounding, dateValue(r.body["issueDate"], false), dateValue(r.body["dueDate"], false), nullable(r.body["notes"])
	i, err := r.insertRow("Invoice", data)
	if err != nil {
		return nil, err
	}
	for _, l := range lines {
		if _, err = r.insertInvoiceLine(i["id"], l); err != nil {
			return nil, err
		}
	}
	if err = r.syncInvoiceCommission(idValue(i["id"]), partner); err != nil {
		return nil, err
	}
	full, err := r.loadInvoice(idValue(i["id"]), false)
	if err != nil {
		return nil, err
	}
	for _, line := range objects(full["lines"]) {
		delete(line, "shipment")
	}
	i["lines"], i["commission"] = full["lines"], nil
	if full["commission"] != nil {
		m := object(full["commission"])
		i["commission"] = map[string]any{"id": m["id"], "commissionCode": m["commissionCode"], "commissionAmount": m["commissionAmount"], "status": m["status"]}
	}
	b.audit(r, "created", "invoice", i["id"], code, nil, map[string]any{"customer": c["name"], "lines": len(lines)})
	return i, nil
}
func (b *business) updateInvoice(r *request) (any, error) {
	i, err := r.loadInvoice(idValue(r.c.Params("id")), true)
	if err != nil {
		return nil, err
	}
	if i["status"] != "DRAFT" {
		return nil, problem(422, "Hanya invoice DRAFT yang bisa diubah.")
	}
	data := map[string]any{}
	if raw, ok := r.body["bankAccountId"]; ok {
		data, err = r.invoiceBank(raw)
		if err != nil {
			return nil, err
		}
	}
	for _, k := range []string{"issueDate", "dueDate"} {
		if raw, ok := r.body[k]; ok {
			data[k] = dateValue(raw, false)
		}
	}
	if raw, ok := r.body["notes"]; ok {
		data["notes"] = nullable(raw)
	}
	for _, k := range []string{"discount", "rounding"} {
		if raw, ok := r.body[k]; ok {
			data[k] = number(raw)
		}
	}
	if number(data["discount"]) < 0 {
		return nil, problem(422, "Diskon tidak boleh negatif.")
	}
	lines := objects(i["lines"])
	if raw, ok := r.body["lines"]; ok {
		lines, err = invoiceLines(raw)
		if err != nil {
			return nil, err
		}
	}
	partner, err := r.validateInvoiceLines(lines, i["customerId"], idValue(i["id"]))
	if err != nil {
		return nil, err
	}
	if _, ok := r.body["lines"]; ok {
		if err = r.exec(`DELETE FROM "InvoiceLine" WHERE "invoiceId"=$1`, i["id"]); err != nil {
			return nil, err
		}
		for _, l := range lines {
			if _, err = r.insertInvoiceLine(i["id"], l); err != nil {
				return nil, err
			}
		}
	}
	updated, err := r.updateRow("Invoice", idValue(i["id"]), data)
	if err != nil {
		return nil, err
	}
	if err = r.syncInvoiceCommission(idValue(i["id"]), partner); err != nil {
		return nil, err
	}
	b.audit(r, "updated", "invoice", i["id"], textValue(i["invoiceNumber"]), nil, data)
	return updated, nil
}
func (b *business) deleteInvoice(r *request) (any, error) {
	i, err := r.loadInvoice(idValue(r.c.Params("id")), true)
	if err != nil {
		return nil, err
	}
	if len(objects(i["settlements"])) > 0 {
		return nil, problem(422, "Invoice sudah memiliki settlement - batalkan (cancel), jangan hapus.")
	}
	if err = r.exec(`DELETE FROM "Invoice" WHERE id=$1`, i["id"]); err != nil {
		return nil, err
	}
	b.audit(r, "deleted", "invoice", i["id"], textValue(i["invoiceNumber"]), i, nil)
	return map[string]any{"deleted": true}, nil
}
func (b *business) addInvoiceLine(r *request) (any, error) {
	i, err := r.loadInvoice(idValue(r.c.Params("id")), true)
	if err != nil {
		return nil, err
	}
	if i["status"] != "DRAFT" {
		return nil, problem(422, "Baris item hanya bisa ditambah pada invoice DRAFT.")
	}
	lines, err := invoiceLines([]any{r.body})
	if err != nil {
		return nil, err
	}
	if number(lines[0]["unitPrice"]) <= 0 {
		return nil, problem(422, "Harga satuan harus lebih dari 0.")
	}
	all := append(objects(i["lines"]), lines[0])
	partner, err := r.validateInvoiceLines(all, i["customerId"], idValue(i["id"]))
	if err != nil {
		return nil, err
	}
	line, err := r.insertInvoiceLine(i["id"], lines[0])
	if err != nil {
		return nil, err
	}
	if err = r.syncInvoiceCommission(idValue(i["id"]), partner); err != nil {
		return nil, err
	}
	b.audit(r, "created", "invoice_line", line["id"], textValue(i["invoiceNumber"]), nil, line)
	return line, nil
}
func (b *business) deleteInvoiceLine(r *request) (any, error) {
	l, err := r.find("InvoiceLine", "Baris item tidak ditemukan.", false)
	if err != nil {
		return nil, err
	}
	i, err := r.loadInvoice(idValue(l["invoiceId"]), true)
	if err != nil {
		return nil, err
	}
	if i["status"] != "DRAFT" {
		return nil, problem(422, "Baris item hanya bisa dihapus pada invoice DRAFT.")
	}
	if err = r.exec(`DELETE FROM "InvoiceLine" WHERE id=$1`, l["id"]); err != nil {
		return nil, err
	}
	lines := []map[string]any{}
	for _, row := range objects(i["lines"]) {
		if idValue(row["id"]) != idValue(l["id"]) {
			lines = append(lines, row)
		}
	}
	partner, err := r.validateInvoiceLines(lines, i["customerId"], idValue(i["id"]))
	if err != nil {
		return nil, err
	}
	if err = r.syncInvoiceCommission(idValue(i["id"]), partner); err != nil {
		return nil, err
	}
	b.audit(r, "deleted", "invoice_line", l["id"], textValue(i["invoiceNumber"]), l, nil)
	return map[string]any{"deleted": true}, nil
}
func (b *business) sendInvoice(r *request) (any, error) {
	i, err := r.loadInvoice(idValue(r.c.Params("id")), true)
	if err != nil {
		return nil, err
	}
	if i["status"] != "DRAFT" || len(objects(i["lines"])) == 0 {
		return nil, problem(422, "Hanya invoice DRAFT dengan baris item yang bisa dikirim.")
	}
	issue := fallback(dateValue(r.body["issueDate"], false), fallback(dateValue(i["issueDate"], false), time.Now().UTC()))
	due := fallback(dateValue(r.body["dueDate"], false), fallback(dateValue(i["dueDate"], false), time.Now().UTC().AddDate(0, 0, 14)))
	updated, err := r.updateRow("Invoice", idValue(i["id"]), map[string]any{"status": "SENT", "issueDate": issue, "dueDate": due})
	if err != nil {
		return nil, err
	}
	b.audit(r, "status_change", "invoice", i["id"], textValue(i["invoiceNumber"])+" → SENT", nil, nil)
	return updated, nil
}
func (b *business) printInvoice(r *request) (any, error) {
	i, err := r.find("Invoice", "Invoice tidak ditemukan.", false)
	if err != nil {
		return nil, err
	}
	roles := []any{}
	if truthy(r.actor["isOwner"]) {
		roles = append(roles, "Owner")
	} else {
		for _, role := range objects(r.actor["roles"]) {
			roles = append(roles, role["name"])
		}
	}
	b.audit(r, "printed_invoice", "invoice", i["id"], textValue(i["invoiceNumber"]), nil, map[string]any{"printedAt": time.Now().UTC(), "accountName": r.actor["name"], "roles": roles})
	return map[string]any{"logged": true}, nil
}

var settlementProof = regexp.MustCompile(`^data:(image/[a-zA-Z0-9.+-]+|application/pdf);base64,`)

func settlementData(body map[string]any) (map[string]any, error) {
	amount, err := minimum(body, "amount", 0)
	if err != nil {
		return nil, err
	}
	if amount > 0 && amount < 0.01 {
		return nil, problem(422, "Jumlah pembayaran minimal 0.01.")
	}
	pph := 0.0
	if raw := body["pph"]; raw != nil && raw != "" {
		pph, err = minimum(body, "pph", 0)
		if err != nil {
			return nil, err
		}
	}
	if pph > amount {
		return nil, problem(422, "PPh tidak boleh melebihi jumlah pembayaran.")
	}
	if body["method"] != "TRANSFER" && body["method"] != "CASH" {
		return nil, problem(422, "Metode pembayaran tidak valid.")
	}
	at := dateValue(body["settledAt"], false)
	if truthy(body["settledAt"]) && at == nil {
		return nil, problem(422, "Tanggal pembayaran tidak valid.")
	}
	data := map[string]any{"amount": amount, "pph": pph, "method": body["method"], "reference": nullable(body["reference"]), "settledAt": fallback(at, time.Now().UTC())}
	if raw, ok := body["proofUrl"]; ok {
		proof := textValue(raw)
		if proof != "" && (!settlementProof.MatchString(proof) || len(proof) > 12000000) {
			return nil, problem(422, "Bukti pembayaran harus gambar/PDF, maksimal 8 MB.")
		}
		data["proofUrl"] = nullable(raw)
	}
	return data, nil
}
func (b *business) mutateInvoiceSettlement(r *request) (any, error) {
	if err := r.exec(`SET TRANSACTION ISOLATION LEVEL SERIALIZABLE`); err != nil {
		return nil, err
	}
	i, err := r.loadInvoice(idValue(r.c.Params("id")), true)
	if err != nil {
		return nil, err
	}
	if i["status"] == "DRAFT" || i["status"] == "CANCELLED" {
		return nil, problem(422, "Invoice berstatus "+textValue(i["status"])+" tidak bisa di-settle.")
	}
	create, remove := r.c.Method() == "POST", r.c.Method() == "DELETE"
	sid := idValue(r.c.Params("settlementId"))
	var previous map[string]any
	paid := 0.0
	count := 0
	for _, s := range objects(i["settlements"]) {
		if !create && idValue(s["id"]) == sid {
			previous = s
			continue
		}
		paid += number(s["amount"])
		count++
	}
	if !create && previous == nil {
		return nil, problem(404, "Settlement tidak ditemukan pada invoice ini.")
	}
	var data, settlement map[string]any
	total := number(i["totalAmount"])
	if !remove {
		data, err = settlementData(r.body)
		if err != nil {
			return nil, err
		}
		amount := number(data["amount"])
		if (amount == 0 && total != 0) || (total == 0 && amount > 0) || (create && total == 0 && count > 0) {
			return nil, problem(422, "Jumlah settlement tidak sesuai total invoice.")
		}
		paid += amount
		count++
		if paid > total+0.01 {
			return nil, problem(422, "Pembayaran melebihi sisa tagihan.")
		}
		if create {
			settlement, err = r.one(`INSERT INTO "InvoiceSettlement" AS s ("invoiceId",amount,pph,method,reference,"proofUrl","settledAt","recordedById") VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING to_jsonb(s)`, i["id"], data["amount"], data["pph"], data["method"], data["reference"], data["proofUrl"], data["settledAt"], r.actor["id"])
		} else {
			proof := previous["proofUrl"]
			if v, ok := data["proofUrl"]; ok {
				proof = v
			}
			settlement, err = r.one(`UPDATE "InvoiceSettlement" s SET amount=$2,pph=$3,method=$4,reference=$5,"proofUrl"=$6,"settledAt"=$7 WHERE id=$1 RETURNING to_jsonb(s)`, sid, data["amount"], data["pph"], data["method"], data["reference"], proof, data["settledAt"])
		}
	} else {
		err = r.exec(`DELETE FROM "InvoiceSettlement" WHERE id=$1`, sid)
	}
	if err != nil {
		return nil, err
	}
	fully := count > 0 && paid >= total-0.01
	status := "SENT"
	if fully {
		status = "SETTLED"
	} else if count > 0 {
		status = "PARTIALLY_SETTLED"
	}
	updated, err := r.updateRow("Invoice", idValue(i["id"]), map[string]any{"status": status})
	if err != nil {
		return nil, err
	}
	released := false
	m := object(i["commission"])
	if i["commission"] != nil && ((fully && m["status"] == "PENDING") || (!fully && m["status"] == "RELEASED")) {
		direction, kind, phase, next := "CREDIT", "COMMISSION", "RELEASE", "RELEASED"
		if !fully {
			direction, kind, phase, next = "DEBIT", "ADJUSTMENT", "REVERSAL", "PENDING"
		}
		ledger, e := r.ledger(m["partnerId"], kind, direction, number(m["commissionAmount"]), "commission", m["id"], fmt.Sprintf("COMM-%d-%s-%d", idValue(i["id"]), phase, time.Now().UnixNano()), "Komisi B2B invoice "+textValue(i["invoiceNumber"]), !fully)
		if e != nil {
			return nil, e
		}
		var at, tx any
		if fully {
			at, tx = time.Now().UTC(), ledger["id"]
		}
		if _, err = r.updateRow("MarketingCommission", idValue(m["id"]), map[string]any{"status": next, "releasedAt": at, "walletTransactionId": tx}); err != nil {
			return nil, err
		}
		released = fully
	}
	snapshot := func(s map[string]any) any {
		if s == nil {
			return nil
		}
		return map[string]any{"id": s["id"], "amount": s["amount"], "pph": s["pph"], "method": s["method"], "reference": s["reference"], "settledAt": s["settledAt"], "proofAttached": truthy(s["proofUrl"])}
	}
	action := "settled"
	if !create {
		action = "settlement_updated"
		if remove {
			action = "settlement_deleted"
		}
	}
	b.audit(r, action, "invoice", i["id"], textValue(i["invoiceNumber"]), snapshot(previous), map[string]any{"settlement": snapshot(settlement), "status": status, "commissionReleased": released})
	result := map[string]any{"invoice": updated, "commissionReleased": released}
	if settlement != nil {
		result["settlement"] = settlement
	}
	if !create {
		result["deleted"] = remove
	}
	return result, nil
}
