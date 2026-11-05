package httpapi

import "github.com/gofiber/fiber/v3"

func (b *business) options(r *request) (any, error) {
	if err := expireTariffs(r); err != nil {
		return nil, err
	}
	result := fiber.Map{"company": fiber.Map{"name": r.company}}
	for _, query := range []struct{ key, sql string }{
		{"employees", `SELECT to_jsonb(t) FROM (SELECT id,name,position,"warehouseId" FROM "Employee" WHERE "isActive" ORDER BY name) t`},
		{"vehicles", `SELECT to_jsonb(t) FROM (SELECT id,"vehicleNumber",name,"maxWeightKg","maxVolumeM3","maxKoli","lengthM","widthM","heightM" FROM "Vehicle" WHERE status='ACTIVE' ORDER BY "vehicleNumber") t`},
		{"routes", `SELECT jsonb_build_object('id',r.id,'name',r.name,'origin',r.origin,'destination',r.destination,'checkpoints',COALESCE((SELECT jsonb_agg(jsonb_build_object('name',c.name,'sequence',c.sequence,'latitude',c.latitude,'longitude',c.longitude,'radiusMeters',c."radiusMeters") ORDER BY c.sequence) FROM "Checkpoint" c WHERE c."routeId"=r.id AND c."isActive"),'[]'::jsonb)) FROM "Route" r WHERE r."isActive" ORDER BY r.name,r.id`},
		{"warehouses", `SELECT to_jsonb(t) FROM (SELECT id,code,name,city,"customerSupportContact" FROM "Warehouse" WHERE "isActive" ORDER BY name) t`},
		{"tariffs", `SELECT to_jsonb(t) FROM (SELECT id,name,origin,destination,"customerType","customerId","ratePerKg","ratePerKoli","ratePerCubic","minChargeableKg","minChargeableKoli","minChargeableM3","volumetricMultiplier","roundingMode","roundingUnitKg","pricingMethod","effectiveFrom","effectiveTo" FROM "Tariff" WHERE "isActive" AND "effectiveFrom"<=CURRENT_TIMESTAMP AND ("effectiveTo" IS NULL OR "effectiveTo">=CURRENT_TIMESTAMP) ORDER BY origin,destination,"customerType") t`},
		{"permissions", `SELECT to_jsonb(t) FROM (SELECT id,slug,module,description FROM "Permission" ORDER BY module,slug) t`},
		{"vehicleOwners", `SELECT jsonb_build_object('id',p.id,'name',u.name,'username',u.username,'profitShare',jsonb_build_object('company',p."companyPercent",'partner',p."partnerPercent")) FROM "Partner" p JOIN "User" u ON u.id=p."userId" WHERE p.type='VEHICLE_OWNER' AND p."isActive" ORDER BY p."createdAt"`},
		{"marketingPartners", `SELECT jsonb_build_object('id',p.id,'name',u.name,'username',u.username,'warehouseId',p."warehouseId",'warehouseName',w.name) FROM "Partner" p JOIN "User" u ON u.id=p."userId" LEFT JOIN "Warehouse" w ON w.id=p."warehouseId" WHERE p.type='MARKETING' AND p."isActive" ORDER BY p."createdAt"`},
	} {
		rows, err := r.many(query.sql)
		if err != nil {
			return nil, err
		}
		result[query.key] = rows
	}
	unscoped, warehouse := warehouseScope(r.actor)
	partner := marketingID(r.actor)
	customers, err := r.many(`SELECT jsonb_build_object('id',c.id,'code',c.code,'name',c.name,'type',c.type,'companyName',c."companyName",'phone',c.phone,'email',c.email,'address',c.address,'marketingPartnerId',c."marketingPartnerId",'warehouseId',c."warehouseId",'warehouseName',w.name) FROM "Customer" c LEFT JOIN "Warehouse" w ON w.id=c."warehouseId" WHERE c."isActive" AND ($1::int IS NULL OR c."marketingPartnerId"=$1) AND ($2 OR c."warehouseId" IS NULL OR c."warehouseId"=$3::int) ORDER BY c.name`, partner, unscoped || partner != nil, warehouse)
	if err != nil {
		return nil, err
	}
	result["customers"] = customers
	shipments, err := r.many(`SELECT jsonb_build_object('id',s.id,'masterCode',s."masterCode",'priceAmount',s."priceAmount",'finalPriceAmount',s."finalPriceAmount",'customerId',s."customerId",'createdByPartnerId',s."createdByPartnerId",'origin',s.origin,'destination',s.destination,
 'invoiceLines',COALESCE((SELECT jsonb_agg(jsonb_build_object('invoice',jsonb_build_object('id',i.id,'invoiceNumber',i."invoiceNumber",'status',i.status))) FROM "InvoiceLine" line JOIN "Invoice" i ON i.id=line."invoiceId" WHERE line."shipmentId"=s.id),'[]'::jsonb))
 FROM "MasterShipment" s JOIN "Customer" c ON c.id=s."customerId" WHERE c.type='b2b' AND s.status<>'CANCELLED' AND EXISTS (SELECT 1 FROM "TransportShipment" ts JOIN "Transport" t ON t.id=ts."transportId" WHERE ts."shipmentId"=s.id AND t.status IN ('ARRIVED','COMPLETED')) AND ($1 OR s."createdByPartnerId"=$2::int) ORDER BY s."createdAt" DESC`, r.actor["partnerType"] != "MARKETING", partner)
	if err != nil {
		return nil, err
	}
	result["b2bShipments"] = shipments
	return result, nil
}
