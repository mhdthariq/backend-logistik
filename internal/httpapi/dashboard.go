package httpapi

func dashboardRole(actor map[string]any) string {
	if truthy(actor["isOwner"]) {
		return "owner"
	}
	if actor["partnerType"] == "MARKETING" {
		return "marketing"
	}
	for _, slug := range []string{"admin-kantor", "admin-gudang", "staff-gudang"} {
		if hasRole(actor, slug) {
			return slug
		}
	}
	return "admin-kantor"
}

func (b *business) dashboard(r *request) (any, error) {
	period, from, to, err := dashboardPeriod(r)
	if err != nil {
		return nil, err
	}
	role := dashboardRole(r.actor)
	marketing := role == "marketing"
	can := func(p string) bool { return hasPermission(r.actor, p) }
	warehouses, err := r.many(`SELECT to_jsonb(w) FROM "Warehouse" w WHERE "isActive"`)
	if err != nil {
		return nil, err
	}
	counts := map[string]any{}
	for _, q := range []struct {
		key, permission, sql string
		args                 []any
	}{
		{"customers", "customer.view", `SELECT count(*) FROM "Customer" WHERE "isActive" AND "createdAt">=$1 AND "createdAt"<=$2 AND (NOT $3 OR $4::int IS NULL OR "marketingPartnerId"=$4)`, []any{from, to, marketing, r.actor["partnerId"]}},
		{"vehicles", "vehicle.view", `SELECT count(*) FROM "Vehicle"`, nil},
		{"gudang", "warehouse.view", `SELECT count(*) FROM "Warehouse" WHERE "isActive"`, nil},
		{"routes", "checkpoint.view", `SELECT count(*) FROM "Route" WHERE "isActive"`, nil},
		{"invoices", "invoice.view", `SELECT count(*) FROM "Invoice" WHERE "createdAt">=$1 AND "createdAt"<=$2`, []any{from, to}},
		{"unpaidInvoices", "invoice.view", `SELECT count(*) FROM "Invoice" WHERE status IN ('SENT','PARTIALLY_SETTLED')`, nil},
		{"activeTariffs", "tariff.view", `SELECT count(*) FROM "Tariff" WHERE "isActive"`, nil},
	} {
		counts[q.key] = nil
		if can(q.permission) {
			var n int
			if err = r.db.QueryRow(r.ctx, q.sql, q.args...).Scan(&n); err != nil {
				return nil, err
			}
			counts[q.key] = n
		}
	}
	shipments := []map[string]any{}
	statusCounts := map[string]int{}
	visibleIDs := []int{}
	counts["shipments"] = nil
	if can("shipment.view") {
		rows, err := r.many(`SELECT to_jsonb(s)||jsonb_build_object('customerName',COALESCE(NULLIF(c."companyName",''),c.name)) FROM "MasterShipment" s JOIN "Customer" c ON c.id=s."customerId" WHERE s."createdAt">=$1 AND s."createdAt"<=$2 AND (NOT $3 OR $4::int IS NULL OR s."createdByPartnerId"=$4) ORDER BY s."createdAt" DESC,s.id DESC`, from, to, marketing, r.actor["partnerId"])
		if err != nil {
			return nil, err
		}
		for _, s := range rows {
			if !marketing && !warehouseIDsVisible(r.actor, shipmentWarehouseIDs(s, warehouses)) {
				continue
			}
			visibleIDs = append(visibleIDs, idValue(s["id"]))
			statusCounts[textValue(s["status"])]++
			item := map[string]any{}
			for _, k := range []string{"id", "masterCode", "status", "origin", "destination", "customerName", "createdAt"} {
				item[k] = s[k]
			}
			shipments = append(shipments, item)
		}
		counts["shipments"] = len(shipments)
	}
	for _, q := range []struct {
		key, permission, sql string
		pickup               bool
	}{
		{"activePickups", "pickup.view", `SELECT to_jsonb(s) FROM "Pickup" p JOIN "MasterShipment" s ON s.id=p."masterId" WHERE p.status IN ('ASSIGNED','IN_PROGRESS','PICKED_UP') AND p."createdAt">=$1 AND p."createdAt"<=$2`, true},
		{"pendingDeliveries", "delivery.view", `SELECT to_jsonb(s) FROM "Delivery" d JOIN "MasterShipment" s ON s.id=d."masterId" WHERE d.status='ASSIGNED' AND d."createdAt">=$1 AND d."createdAt"<=$2`, false},
	} {
		counts[q.key] = nil
		if !can(q.permission) {
			continue
		}
		rows, err := r.many(q.sql, from, to)
		if err != nil {
			return nil, err
		}
		n := 0
		for _, s := range rows {
			if q.pickup {
				s["status"], s["arrivedWarehouseId"] = "CREATED", nil
			}
			if marketing || warehouseIDsVisible(r.actor, shipmentWarehouseIDs(s, warehouses)) {
				n++
			}
		}
		counts[q.key] = n
	}
	var capacitySummary any
	counts["inTransit"] = nil
	if can("transport.view") {
		rows, err := r.many(dashboardTransportSelect + `WHERE t.status IN ('PLANNED','DEPARTED') ORDER BY t.id`)
		if err != nil {
			return nil, err
		}
		summary := map[string]int{"activeTransports": 0, "overCapacityTransports": 0, "warningTransports": 0, "partiallyConfiguredTransports": 0, "vehiclesOverWeight": 0, "vehiclesOverVolume": 0, "vehiclesOverKoli": 0}
		inTransit := 0
		warning := threshold(r)
		for i, t := range rows {
			ships := objects(t["shipments"])
			t["shipments"] = ships
			if t["status"] == "DEPARTED" && inDashboardPeriod(t["departedAt"], from, to) && (marketing || warehouseIDsVisible(r.actor, transportGudangIDs(t, warehouses))) {
				inTransit++
			}
			if i >= 200 {
				continue
			}
			_, load := loadTotals(ships)
			cap := vehicleCapacity(object(t["vehicle"]), load, warning)
			summary["activeTransports"]++
			switch cap.Overall {
			case "OVERLIMIT":
				summary["overCapacityTransports"]++
			case "WARNING":
				summary["warningTransports"]++
			case "PARTIALLY_CONFIGURED":
				summary["partiallyConfiguredTransports"]++
			}
			if cap.Weight.Status == "OVERLIMIT" {
				summary["vehiclesOverWeight"]++
			}
			if cap.Volume.Status == "OVERLIMIT" {
				summary["vehiclesOverVolume"]++
			}
			if cap.Koli.Status == "OVERLIMIT" {
				summary["vehiclesOverKoli"]++
			}
		}
		counts["inTransit"], capacitySummary = inTransit, summary
	}
	permissions := map[string]bool{"canVerifyTopup": can("wallet.topup.verify"), "canApproveWithdrawal": can("wallet.withdrawal.approve"), "canReviewWithdrawal": can("wallet.withdrawal.view"), "canVerifyPayment": can("payment.verify"), "canReleaseCommission": can("invoice.send")}
	approvals, err := dashboardApprovals(r, permissions, from, to)
	if err != nil {
		return nil, err
	}
	recentAudit := []map[string]any{}
	if can("audit_log.view") {
		take := 100
		unscoped, _ := warehouseScope(r.actor)
		if unscoped {
			take = 8
		}
		rows, err := r.many(auditSelect+`WHERE a."createdAt">=$1 AND a."createdAt"<=$2 ORDER BY a."createdAt" DESC,a.id DESC LIMIT $3`, from, to, take)
		if err != nil {
			return nil, err
		}
		rows = r.visibleAudits(rows, warehouses)
		for _, row := range rows[:min(8, len(rows))] {
			item := map[string]any{}
			for _, k := range []string{"id", "action", "entityType", "entityLabel", "actorName", "createdAt"} {
				item[k] = row[k]
			}
			recentAudit = append(recentAudit, item)
		}
	}
	unscoped, _ := warehouseScope(r.actor)
	var revenue float64
	if err = r.db.QueryRow(r.ctx, `SELECT COALESCE(sum(amount),0) FROM "Payment" WHERE status='VERIFIED' AND "verifiedAt">=$1 AND "verifiedAt"<=$2 AND ($3 OR "masterId"=ANY($4::int[]))`, from, to, unscoped || marketing, visibleIDs).Scan(&revenue); err != nil {
		return nil, err
	}
	var marketingSnapshot, workspace any
	if marketing && r.actor["partnerId"] != nil {
		marketingSnapshot, err = dashboardMarketing(r)
		if err != nil {
			return nil, err
		}
	}
	if role == "admin-gudang" || role == "staff-gudang" || truthy(r.actor["isOwner"]) {
		workspace, err = b.dashboardGudang(r)
		if err != nil {
			return nil, err
		}
	}
	return map[string]any{"role": role, "permissions": permissions, "period": period, "approvals": approvals, "counts": counts, "statusCounts": statusCounts, "revenueVerified": revenue, "recentShipments": shipments[:min(6, len(shipments))], "recentAudit": recentAudit, "marketing": marketingSnapshot, "gudang": workspace, "capacitySummary": capacitySummary}, nil
}

func (b *business) dashboardGudang(r *request) (any, error) {
	data, err := b.gudang(r)
	if err != nil {
		return nil, err
	}
	w := data.(map[string]any)
	held := []map[string]any{}
	for _, warehouse := range w["warehouses"].([]map[string]any) {
		unpaid := 0
		for _, s := range warehouse["shipments"].([]map[string]any) {
			if number(s["remainingAmount"]) > 0.01 {
				unpaid++
			}
		}
		held = append(held, map[string]any{"warehouseId": warehouse["id"], "warehouseName": warehouse["name"], "city": warehouse["city"], "heldShipments": warehouse["heldShipments"], "heldPackages": warehouse["heldPackages"], "heldWeightKg": warehouse["heldWeightKg"], "unpaidCount": unpaid})
	}
	for _, key := range []string{"arrivals", "transportArrivals", "walkIns"} {
		rows := w[key].([]map[string]any)
		fields := []string{"id", "masterCode", "customerName", "origin", "destination", "detailsCount"}
		switch key {
		case "arrivals":
			fields = append(fields, "scannedCount", "pickupCode", "kurirName", "updatedAt")
		case "transportArrivals":
			fields = append(fields, "originWarehouseName", "transportCode", "driverName", "scannedCount", "updatedAt")
		case "walkIns":
			fields = append(fields, "status", "totalWeightKg")
		}
		items := []map[string]any{}
		limit := len(rows)
		if key != "arrivals" {
			limit = min(10, limit)
		}
		for _, row := range rows[:limit] {
			item := map[string]any{}
			for _, k := range fields {
				item[k] = row[k]
			}
			items = append(items, item)
		}
		w[key] = items
	}
	delete(w, "warehouses")
	w["heldSummary"] = held
	return w, nil
}

func dashboardMarketing(r *request) (any, error) {
	id := r.actor["partnerId"]
	summary, err := r.walletSummary(id)
	if err != nil {
		return nil, err
	}
	for _, k := range []string{"partnerId", "walletId"} {
		delete(summary, k)
	}
	commissions, err := r.many(`SELECT jsonb_build_object('id',m.id,'commissionCode',m."commissionCode",'status',m.status,'amount',m."commissionAmount",'invoiceNumber',COALESCE(i."invoiceNumber",'-'),'invoiceStatus',COALESCE(i.status,'-'),'customerName',COALESCE(NULLIF(c."companyName",''),c.name,'-'),'createdAt',m."createdAt") FROM "MarketingCommission" m LEFT JOIN "Invoice" i ON i.id=m."invoiceId" LEFT JOIN "Customer" c ON c.id=i."customerId" WHERE m."partnerId"=$1 ORDER BY m."createdAt" DESC,m.id DESC LIMIT 5`, id)
	if err != nil {
		return nil, err
	}
	pending := 0.0
	for _, c := range commissions {
		if c["status"] == "PENDING" {
			pending += number(c["amount"])
		}
	}
	txs, err := r.many(`SELECT jsonb_build_object('id',t.id,'type',t.type,'amount',t.amount,'direction',t.direction,'description',t.description,'businessRef',t."businessRef",'createdAt',t."createdAt") FROM "WalletTransaction" t JOIN "Wallet" w ON w.id=t."walletId" WHERE w."partnerId"=$1 ORDER BY t."createdAt" DESC,t.id DESC LIMIT 5`, id)
	if err != nil {
		return nil, err
	}
	withdrawals, err := r.many(`SELECT jsonb_build_object('id',id,'requestCode',"requestCode",'amount',amount,'status',status,'createdAt',"createdAt") FROM "WithdrawalRequest" WHERE "partnerId"=$1 AND status IN ('PENDING','APPROVED','PROCESSING') ORDER BY "createdAt" DESC,id DESC LIMIT 5`, id)
	if err != nil {
		return nil, err
	}
	var released float64
	if err = r.db.QueryRow(r.ctx, `SELECT COALESCE(sum(t.amount),0) FROM "WalletTransaction" t JOIN "Wallet" w ON w.id=t."walletId" WHERE w."partnerId"=$1 AND t.type='COMMISSION' AND t.direction='CREDIT'`, id).Scan(&released); err != nil {
		return nil, err
	}
	return map[string]any{"wallet": summary, "pendingCommission": pending, "releasedCommission": released, "recentCommissions": commissions, "recentTransactions": txs, "pendingWithdrawals": withdrawals}, nil
}
