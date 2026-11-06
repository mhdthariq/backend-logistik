package httpapi

import "github.com/gofiber/fiber/v3"

func (b *business) registerPartnerViews(app *fiber.App) {
	app.Get("/api/v1/commissions", b.handle("wallet.view_own", true, false, b.commissions))
	app.Get("/api/v1/partner/vehicles", b.handle("vehicle.view_own", true, false, b.partnerVehicles))
	app.Get("/api/v1/partner/transports", b.handle("transport.view_own_vehicles", true, false, b.partnerTransports))
	app.Get("/api/v1/partner/dashboard", b.handle("wallet.view_own", true, true, b.partnerDashboard))
}
func (b *business) commissions(r *request) (any, error) {
	id, err := r.ownPartner("MARKETING")
	if err != nil {
		return nil, err
	}
	return r.many(`SELECT to_jsonb(m)||jsonb_build_object('invoiceId',i.id,'invoiceNumber',i."invoiceNumber",'invoiceStatus',i.status,'customerName',COALESCE(c."companyName",c.name),'paidAmount',s.paid,'remainingAmount',GREATEST(0,l.subtotal-i.discount+i.rounding-s.paid),'shipments',l.shipments) - 'walletTransactionId' - 'updatedAt' FROM "MarketingCommission" m JOIN "Invoice" i ON i.id=m."invoiceId" JOIN "Customer" c ON c.id=i."customerId" CROSS JOIN LATERAL (SELECT COALESCE(sum(amount),0) paid FROM "InvoiceSettlement" WHERE "invoiceId"=i.id) s CROSS JOIN LATERAL (SELECT COALESCE(sum(quantity*"unitPrice"),0) subtotal,count("shipmentId") shipments FROM "InvoiceLine" WHERE "invoiceId"=i.id) l WHERE m."partnerId"=$1 AND ($2='' OR m.status=$2) ORDER BY m."createdAt" DESC,m.id DESC`, id, r.c.Query("status"))
}
func (b *business) partnerVehicles(r *request) (any, error) {
	id, err := r.ownPartner("VEHICLE_OWNER")
	if err != nil {
		return nil, err
	}
	return r.many(`SELECT to_jsonb(v)||jsonb_build_object('_count',jsonb_build_object('transports',(SELECT count(*) FROM "Transport" WHERE "vehicleId"=v.id),'repairs',(SELECT count(*) FROM "VehicleRepair" WHERE "vehicleId"=v.id),'settlements',(SELECT count(*) FROM "TransportSettlement" WHERE "vehicleId"=v.id))) FROM "Vehicle" v WHERE "ownerId"=$1 ORDER BY "vehicleNumber",id`, id)
}
func (b *business) partnerTransports(r *request) (any, error) {
	id, err := r.ownPartner("VEHICLE_OWNER")
	if err != nil {
		return nil, err
	}
	p, err := r.partnerBrief(id)
	if err != nil {
		return nil, err
	}
	rows, err := r.many(`SELECT to_jsonb(t)||jsonb_build_object('vehicle',to_jsonb(v),'routeName',rt.name,'settlement',to_jsonb(s),'shipments',COALESCE((SELECT jsonb_agg(jsonb_build_object('master',to_jsonb(m),'packages',(SELECT count(*) FROM "DetailShipment" d WHERE d."masterId"=m.id),'actualWeight',(SELECT COALESCE(sum(d."actualWeightKg"),0) FROM "DetailShipment" d WHERE d."masterId"=m.id),'volume',(SELECT COALESCE(sum(COALESCE(d."lengthCm",0)*COALESCE(d."widthCm",0)*COALESCE(d."heightCm",0)/1000000.0),0) FROM "DetailShipment" d WHERE d."masterId"=m.id))) FROM "TransportShipment" ts JOIN "MasterShipment" m ON m.id=ts."shipmentId" WHERE ts."transportId"=t.id),'[]'::jsonb)) FROM "Transport" t JOIN "Vehicle" v ON v.id=t."vehicleId" LEFT JOIN "Route" rt ON rt.id=t."routeId" LEFT JOIN "TransportSettlement" s ON s."transportId"=t.id WHERE v."ownerId"=$1 ORDER BY t."createdAt" DESC,t.id DESC LIMIT 200`, id)
	if err != nil {
		return nil, err
	}
	warning := threshold(r)
	out := []map[string]any{}
	for _, t := range rows {
		ships := objects(t["shipments"])
		totals, load := loadTotals(ships)
		v := object(t["vehicle"])
		s := object(t["settlement"])
		percent, value := p["partnerPercent"], totals["totalPrice"]
		var earnings any
		if value == nil {
			value = 0
		}
		if t["settlement"] != nil {
			percent, value, earnings = s["ownerPercent"], s["transportValue"], s["ownerAmount"]
		}
		route := t["routeName"]
		if route == nil {
			route = textValue(fallback(t["origin"], "-")) + " → " + textValue(fallback(t["destination"], "-"))
		}
		row := map[string]any{"routeName": route, "vehicleId": v["id"], "vehicleNumber": v["vehicleNumber"], "shipmentCount": len(ships), "transportValue": value, "ownerPercent": percent, "ownerEarnings": earnings, "capacity": vehicleCapacity(v, load, warning), "settlement": nil}
		if t["settlement"] != nil {
			snap := map[string]any{}
			for _, k := range []string{"settlementCode", "status", "companyPercent", "ownerPercent", "transportValue", "companyAmount", "ownerAmount", "finalizedAt"} {
				snap[k] = s[k]
			}
			row["settlement"] = snap
		}
		for _, k := range []string{"id", "transportCode", "status", "origin", "destination", "departedAt", "arrivedAt", "createdAt"} {
			row[k] = t[k]
		}
		out = append(out, row)
	}
	return out, nil
}
func (b *business) partnerDashboard(r *request) (any, error) {
	id, err := r.ownPartner("VEHICLE_OWNER")
	if err != nil {
		return nil, err
	}
	summary, err := r.walletSummary(id)
	if err != nil {
		return nil, err
	}
	vehicles, err := r.many(`SELECT jsonb_build_object('id',id,'vehicleNumber',"vehicleNumber",'name',name,'status',status,'maxWeightKg',"maxWeightKg",'maxVolumeM3',"maxVolumeM3") FROM "Vehicle" WHERE "ownerId"=$1 ORDER BY "vehicleNumber",id`, id)
	if err != nil {
		return nil, err
	}
	settlements, err := r.many(`SELECT to_jsonb(s)||jsonb_build_object('transport',jsonb_build_object('transportCode',t."transportCode",'origin',t.origin,'destination',t.destination,'arrivedAt',t."arrivedAt"),'vehicle',jsonb_build_object('vehicleNumber',v."vehicleNumber")) FROM "TransportSettlement" s JOIN "Transport" t ON t.id=s."transportId" JOIN "Vehicle" v ON v.id=s."vehicleId" WHERE s."ownerId"=$1 ORDER BY s."finalizedAt" DESC,s.id DESC LIMIT 5`, id)
	if err != nil {
		return nil, err
	}
	repairs, err := r.many(repairSelect+`WHERE r."ownerId"=$1 AND r.status='VERIFIED' ORDER BY r."repairDate" DESC,r.id DESC`, id)
	if err != nil {
		return nil, err
	}
	for _, row := range repairs {
		delete(row, "owner")
	}
	transactions, err := r.many(`SELECT to_jsonb(t) FROM "WalletTransaction" t JOIN "Wallet" w ON w.id=t."walletId" WHERE w."partnerId"=$1 ORDER BY t."createdAt" DESC,t.id DESC LIMIT 8`, id)
	if err != nil {
		return nil, err
	}
	logs, err := r.many(`SELECT to_jsonb(l) FROM "RepairActionLog" l WHERE "ownerId"=$1 ORDER BY "createdAt" DESC,id DESC LIMIT 5`, id)
	if err != nil {
		return nil, err
	}
	totals, err := r.one(`SELECT jsonb_build_object('earnings',(SELECT COALESCE(sum(t.amount),0) FROM "WalletTransaction" t JOIN "Wallet" w ON w.id=t."walletId" WHERE w."partnerId"=$1 AND t.type='TRANSPORT_PROFIT_SHARE' AND t.direction='CREDIT'),'transportCount',(SELECT count(*) FROM "TransportSettlement" WHERE "ownerId"=$1),'repairDeductions',(SELECT COALESCE(sum(amount),0) FROM "VehicleRepair" WHERE "ownerId"=$1 AND status='VERIFIED'),'withdrawals',(SELECT COALESCE(sum(amount),0) FROM "WithdrawalRequest" WHERE "partnerId"=$1 AND status='COMPLETED'))`, id)
	if err != nil {
		return nil, err
	}
	totals["pendingWithdrawals"] = summary["reserved"]
	breakdown := map[string]int{"ACTIVE": 0, "MAINTENANCE": 0, "INACTIVE": 0, "OTHER": 0}
	for _, v := range vehicles {
		k := textValue(v["status"])
		if _, ok := breakdown[k]; !ok {
			k = "OTHER"
		}
		breakdown[k]++
	}
	return map[string]any{"wallet": summary, "totals": totals, "vehicles": vehicles, "vehicleStatusBreakdown": breakdown, "recentSettlements": settlements, "recentTransactions": transactions, "recentRepairLogs": logs, "repairs": repairs}, nil
}
