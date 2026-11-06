package httpapi

import (
	"math"
	"slices"
	"strconv"

	"github.com/gofiber/fiber/v3"
)

func (b *business) registerFinance(app *fiber.App) {
	app.Get("/api/v1/finance/summary", b.handle("financial.report.view", true, false, b.financeSummary))
	app.Get("/api/v1/finance/operational-expenses", b.handle("financial.report.view", true, false, b.financeExpenses))
}
func (b *business) financeExpenses(r *request) (any, error) {
	page, err := strconv.Atoi(r.c.Query("page", "1"))
	if err != nil || page < 1 || page > 100000 {
		return nil, problem(422, "Halaman tidak valid.")
	}
	kind, status, search := r.c.Query("type"), r.c.Query("status"), r.c.Query("search")
	if kind != "" && !slices.Contains(expenseTypes, kind) {
		return nil, problem(422, "Jenis biaya tidak valid.")
	}
	if status != "" && !slices.Contains([]string{"APPROVED", "PENDING", "CANCELLED"}, status) {
		return nil, problem(422, "Status biaya tidak valid.")
	}
	if jsLength(search) > 150 {
		return nil, problem(422, "Pencarian maksimal 150 karakter.")
	}
	groups, err := r.many(`SELECT jsonb_build_object('type',type,'status',status,'amount',sum(amount),'count',count(*)) FROM "TransportExpense" GROUP BY type,status`)
	if err != nil {
		return nil, err
	}
	labels := map[string]string{"BONGKAR": "Biaya Bongkar", "PARKIR": "Biaya Parkir", "MAKAN": "Uang Makan", "BBM": "Biaya BBM", "ANGKUT": "Biaya Angkut", "TOL": "Biaya Tol"}
	types := []map[string]any{}
	approved, pending := 0.0, 0.0
	for _, k := range expenseTypes {
		a, p, c := 0.0, 0.0, 0.0
		for _, g := range groups {
			if g["type"] == k {
				switch g["status"] {
				case "APPROVED":
					a = number(g["amount"])
					c += number(g["count"])
				case "PENDING":
					p = number(g["amount"])
					c += number(g["count"])
				}
			}
		}
		approved += a
		pending += p
		types = append(types, map[string]any{"type": k, "label": labels[k], "approved": a, "pending": p, "count": c})
	}
	from := ` FROM "TransportExpense" e LEFT JOIN "Transport" t ON t.id=e."transportId" LEFT JOIN "MasterShipment" s ON s.id=e."shipmentId" WHERE ($1='' OR e.type=$1) AND ($2='' OR e.status=$2) AND ($3='' OR strpos(lower(e."expenseCode"),lower($3))>0 OR strpos(lower(e.description),lower($3))>0 OR strpos(lower(s."masterCode"),lower($3))>0 OR strpos(lower(t."transportCode"),lower($3))>0)`
	var count int
	if err = r.db.QueryRow(r.ctx, `SELECT count(*)`+from, kind, status, search).Scan(&count); err != nil {
		return nil, err
	}
	rows, err := r.many(`SELECT jsonb_build_object('id',e.id,'expenseCode',e."expenseCode",'type',e.type,'amount',e.amount,'description',e.description,'status',e.status,'createdAt',e."createdAt",'approvedAt',e."approvedAt",'approvedByUsername',e."approvedByUsername",'approvedByRole',e."approvedByRole",'cancellationReason',e."cancellationReason",'transport',CASE WHEN t.id IS NULL THEN NULL ELSE jsonb_build_object('id',t.id,'transportCode',t."transportCode",'origin',t.origin,'destination',t.destination) END,'shipment',CASE WHEN s.id IS NULL THEN NULL ELSE jsonb_build_object('id',s.id,'masterCode',s."masterCode",'origin',s.origin,'destination',s.destination) END)`+from+` ORDER BY e."createdAt" DESC,e.id DESC LIMIT 25 OFFSET $4`, kind, status, search, (page-1)*25)
	if err != nil {
		return nil, err
	}
	return map[string]any{"approvedTotal": approved, "pendingTotal": pending, "byType": types, "rows": rows, "count": count, "page": page, "pageSize": 25, "pages": max(1, (count+24)/25)}, nil
}
func (b *business) financeSummary(r *request) (any, error) {
	partners, err := r.many(`SELECT jsonb_build_object('id',p.id,'name',u.name,'username',u.username,'type',p.type,'profitShare',jsonb_build_object('company',p."companyPercent",'partner',p."partnerPercent"),'walletBalance',COALESCE(w.balance,0),'vehicleCount',(SELECT count(*) FROM "Vehicle" WHERE "ownerId"=p.id)) FROM "Partner" p JOIN "User" u ON u.id=p."userId" LEFT JOIN "Wallet" w ON w."partnerId"=p.id ORDER BY p."createdAt",p.id`)
	if err != nil {
		return nil, err
	}
	totals, err := r.one(`SELECT jsonb_build_object(
 'totalReserved',(SELECT COALESCE(sum(amount),0) FROM "WithdrawalRequest" WHERE status IN ('PENDING','APPROVED','PROCESSING')),
 'verifiedTopUps',(SELECT COALESCE(sum(amount),0) FROM "TopUpRequest" WHERE status='VERIFIED'),
 'releasedCommissions',(SELECT COALESCE(sum("commissionAmount"),0) FROM "MarketingCommission" WHERE status='RELEASED'),
 'transportSharePaid',(SELECT COALESCE(sum("ownerAmount"),0) FROM "TransportSettlement"),
 'transportValueSettled',(SELECT COALESCE(sum("transportValue"),0) FROM "TransportSettlement"),
 'repairDeductions',(SELECT COALESCE(sum("deductedAmount"),0) FROM "VehicleRepair" WHERE status='VERIFIED' AND "ownerId" IS NOT NULL),
 'withdrawalsCompleted',(SELECT COALESCE(sum(amount),0) FROM "WithdrawalRequest" WHERE status='COMPLETED'),
 'unsettledArrivedTransports',(SELECT count(*) FROM "Transport" t JOIN "Vehicle" v ON v.id=t."vehicleId" WHERE t.status IN ('ARRIVED','COMPLETED') AND v."ownerId" IS NOT NULL AND NOT EXISTS(SELECT 1 FROM "TransportSettlement" s WHERE s."transportId"=t.id)),
 'settledInvoicePayments',(SELECT COALESCE(sum(s.amount),0) FROM "InvoiceSettlement" s JOIN "Invoice" i ON i.id=s."invoiceId" WHERE i.status='SETTLED'),
 'partialInvoicePayments',(SELECT COALESCE(sum(s.amount),0) FROM "InvoiceSettlement" s JOIN "Invoice" i ON i.id=s."invoiceId" WHERE i.status='PARTIALLY_SETTLED'))`)
	if err != nil {
		return nil, err
	}
	marketing, owners, balance := 0, 0, 0.0
	for _, p := range partners {
		if p["type"] == "MARKETING" {
			marketing++
		}
		if p["type"] == "VEHICLE_OWNER" {
			owners++
		}
		balance += number(p["walletBalance"])
	}
	realized := number(totals["settledInvoicePayments"]) + number(totals["partialInvoicePayments"])
	totals["partnerCount"], totals["marketingCount"], totals["vehicleOwnerCount"], totals["totalWalletBalance"], totals["totalAvailable"], totals["realizedInvoicePayments"] = len(partners), marketing, owners, balance, math.Max(0, balance-number(totals["totalReserved"])), realized
	income, err := r.one(`SELECT jsonb_build_object('b2cReceived',COALESCE(sum(p.amount) FILTER(WHERE p.status='VERIFIED'),0),'b2cCash',COALESCE(sum(p.amount) FILTER(WHERE p.status='VERIFIED' AND p.method='CASH'),0),'b2cTransfer',COALESCE(sum(p.amount) FILTER(WHERE p.status='VERIFIED' AND p.method='TRANSFER'),0),'b2cPending',COALESCE(sum(p.amount) FILTER(WHERE p.status='RECORDED'),0)) FROM "Payment" p JOIN "MasterShipment" s ON s.id=p."masterId" JOIN "Customer" c ON c.id=s."customerId" WHERE c.type='b2c'`)
	if err != nil {
		return nil, err
	}
	var outstanding float64
	if err = r.db.QueryRow(r.ctx, `SELECT COALESCE(sum(GREATEST(0,(SELECT COALESCE(sum(l.quantity*l."unitPrice"),0) FROM "InvoiceLine" l WHERE l."invoiceId"=i.id)-i.discount+i.rounding-(SELECT COALESCE(sum(s.amount),0) FROM "InvoiceSettlement" s WHERE s."invoiceId"=i.id))),0) FROM "Invoice" i JOIN "Customer" c ON c.id=i."customerId" WHERE c.type='b2b' AND i.status IN ('SENT','PARTIALLY_SETTLED','SETTLED')`).Scan(&outstanding); err != nil {
		return nil, err
	}
	income["b2bOutstanding"], income["b2bReceived"], income["totalReceived"] = outstanding, realized, realized+number(income["b2cReceived"])
	pending := map[string]any{}
	queries := []struct{ key, query string }{
		{"topUps", `SELECT jsonb_build_object('id',t.id,'requestCode',t."requestCode",'partnerName',u.name,'partnerType',p.type,'amount',t.amount,'status',t.status,'createdAt',t."createdAt") FROM "TopUpRequest" t JOIN "Partner" p ON p.id=t."partnerId" JOIN "User" u ON u.id=p."userId" WHERE t.status='PENDING_VERIFICATION' ORDER BY t."createdAt" DESC,t.id DESC`},
		{"withdrawals", `SELECT jsonb_build_object('id',t.id,'requestCode',t."requestCode",'partnerName',u.name,'partnerType',p.type,'amount',t.amount,'status',t.status,'bankName',t."bankName",'bankAccountNumber',t."bankAccountNumber",'createdAt',t."createdAt") FROM "WithdrawalRequest" t JOIN "Partner" p ON p.id=t."partnerId" JOIN "User" u ON u.id=p."userId" WHERE t.status IN ('PENDING','APPROVED','PROCESSING') ORDER BY t."createdAt" DESC,t.id DESC`},
		{"repairs", `SELECT jsonb_build_object('id',r.id,'repairCode',r."repairCode",'vehicleNumber',v."vehicleNumber",'ownerName',COALESCE(u.name,'Perusahaan'),'amount',r.amount,'status',r.status,'createdAt',r."createdAt") FROM "VehicleRepair" r JOIN "Vehicle" v ON v.id=r."vehicleId" LEFT JOIN "Partner" p ON p.id=r."ownerId" LEFT JOIN "User" u ON u.id=p."userId" ORDER BY r."createdAt" DESC,r.id DESC LIMIT 6`},
		{"commissions", `SELECT jsonb_build_object('id',m.id,'commissionCode',m."commissionCode",'partnerName',u.name,'invoiceNumber',i."invoiceNumber",'commissionAmount',m."commissionAmount",'createdAt',m."createdAt") FROM "MarketingCommission" m JOIN "Partner" p ON p.id=m."partnerId" JOIN "User" u ON u.id=p."userId" JOIN "Invoice" i ON i.id=m."invoiceId" WHERE m.status='PENDING' ORDER BY m."createdAt" DESC,m.id DESC`},
	}
	for _, q := range queries {
		rows, e := r.many(q.query)
		if e != nil {
			return nil, e
		}
		pending[q.key] = rows
	}
	recent, err := r.many(`SELECT jsonb_build_object('id',s.id,'amount',s.amount,'method',s.method,'reference',s.reference,'hasProof',COALESCE(s."proofUrl"<>'',false),'settledAt',s."settledAt",'invoiceNumber',i."invoiceNumber",'invoiceStatus',i.status,'customerName',COALESCE(c."companyName",c.name)) FROM "InvoiceSettlement" s JOIN "Invoice" i ON i.id=s."invoiceId" JOIN "Customer" c ON c.id=i."customerId" ORDER BY s."settledAt" DESC,s.id DESC LIMIT 8`)
	if err != nil {
		return nil, err
	}
	byMonth := []map[string]any{}
	for _, s := range recent {
		month := textValue(s["settledAt"])[:7]
		var bucket map[string]any
		for _, v := range byMonth {
			if v["month"] == month {
				bucket = v
				break
			}
		}
		if bucket == nil {
			bucket = map[string]any{"month": month, "settled": 0.0, "partial": 0.0}
			byMonth = append(byMonth, bucket)
		}
		key := "partial"
		if s["invoiceStatus"] == "SETTLED" {
			key = "settled"
		}
		bucket[key] = number(bucket[key]) + number(s["amount"])
	}
	ledger, err := r.many(`SELECT jsonb_build_object('month',to_char("createdAt",'YYYY-MM'),'type',type,'amount',sum(CASE WHEN direction='CREDIT' THEN amount ELSE -amount END)) FROM "WalletTransaction" WHERE "createdAt">=date_trunc('month',CURRENT_TIMESTAMP)-interval '5 months' GROUP BY to_char("createdAt",'YYYY-MM'),type ORDER BY to_char("createdAt",'YYYY-MM')`)
	if err != nil {
		return nil, err
	}
	monthly := []map[string]any{}
	for _, l := range ledger {
		var bucket map[string]any
		for _, v := range monthly {
			if v["month"] == l["month"] {
				bucket = v
				break
			}
		}
		if bucket == nil {
			bucket = map[string]any{"month": l["month"], "byType": map[string]any{}}
			monthly = append(monthly, bucket)
		}
		object(bucket["byType"])[textValue(l["type"])] = l["amount"]
	}
	return map[string]any{"customerIncome": income, "totals": totals, "pending": pending, "partners": partners, "invoicePayments": map[string]any{"realized": realized, "settledTotal": totals["settledInvoicePayments"], "partialTotal": totals["partialInvoicePayments"], "recent": recent, "byMonth": byMonth}, "monthlyLedger": monthly}, nil
}
