package httpapi

import "time"

func dashboardApprovals(r *request, permissions map[string]bool, from, to time.Time) (map[string]any, error) {
	out := map[string]any{}
	for _, q := range []struct {
		key     string
		allowed bool
		sql     string
		args    []any
	}{
		{"pendingTopups", permissions["canVerifyTopup"], `SELECT jsonb_build_object('id',t.id,'requestCode',t."requestCode",'amount',t.amount,'partnerName',COALESCE(u.name,'-'),'partnerType',COALESCE(p.type,'MARKETING'),'submittedAt',COALESCE(t."submittedForVerificationAt",t."createdAt"),'proofUrl',t."proofUrl") FROM "TopUpRequest" t LEFT JOIN "Partner" p ON p.id=t."partnerId" LEFT JOIN "User" u ON u.id=p."userId" WHERE t.status='PENDING_VERIFICATION' ORDER BY t."submittedForVerificationAt" DESC,t.id DESC LIMIT 10`, nil},
		{"pendingWithdrawals", permissions["canReviewWithdrawal"] && permissions["canApproveWithdrawal"], `SELECT jsonb_build_object('id',w.id,'requestCode',w."requestCode",'amount',w.amount,'partnerName',COALESCE(u.name,'-'),'partnerType',COALESCE(p.type,'VEHICLE_OWNER'),'bankName',w."bankName",'bankAccountName',w."bankAccountName",'bankAccountNumber',w."bankAccountNumber",'createdAt',w."createdAt") FROM "WithdrawalRequest" w LEFT JOIN "Partner" p ON p.id=w."partnerId" LEFT JOIN "User" u ON u.id=p."userId" WHERE w.status='PENDING' ORDER BY w."createdAt" DESC,w.id DESC LIMIT 10`, nil},
		{"pendingPayments", permissions["canVerifyPayment"], `SELECT jsonb_build_object('id',p.id,'amount',p.amount,'method',p.method,'reference',p.reference,'masterCode',COALESCE(s."masterCode",'-'),'customerName',COALESCE(NULLIF(c."companyName",''),c.name,'-'),'recordedByName',COALESCE(u.name,'-'),'createdAt',p."createdAt") FROM "Payment" p LEFT JOIN "MasterShipment" s ON s.id=p."masterId" LEFT JOIN "Customer" c ON c.id=s."customerId" LEFT JOIN "User" u ON u.id=p."recordedById" WHERE p.status='RECORDED' AND p."createdAt">=$1 AND p."createdAt"<=$2 ORDER BY p."createdAt" DESC,p.id DESC LIMIT 10`, []any{from, to}},
		{"pendingCommissions", permissions["canReleaseCommission"], `SELECT jsonb_build_object('id',m.id,'commissionCode',m."commissionCode",'amount',m."commissionAmount",'partnerName',COALESCE(u.name,'-'),'invoiceNumber',COALESCE(i."invoiceNumber",'-'),'invoiceStatus',COALESCE(i.status,'-'),'createdAt',m."createdAt") FROM "MarketingCommission" m LEFT JOIN "Partner" p ON p.id=m."partnerId" LEFT JOIN "User" u ON u.id=p."userId" LEFT JOIN "Invoice" i ON i.id=m."invoiceId" WHERE m.status='PENDING' ORDER BY m."createdAt" DESC,m.id DESC LIMIT 10`, nil},
	} {
		out[q.key] = []map[string]any{}
		if q.allowed {
			rows, err := r.many(q.sql, q.args...)
			if err != nil {
				return nil, err
			}
			out[q.key] = rows
		}
	}
	return out, nil
}
