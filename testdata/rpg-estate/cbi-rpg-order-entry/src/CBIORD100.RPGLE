**free
ctl-opt bnddir('CBISHARED');
/copy ../cbi-rpg-shared-services/copy/CBI_SHARED.rpgleinc

dcl-f ORDHDR usage(*output);
dcl-f ORDLINE usage(*output);
dcl-f ORDERDSP workstn;

dcl-pr PostBilling extpgm('CBIBILLP');
  OrderId char(12) const;
end-pr;

dcl-s OrderId char(12) inz('ORD000120001');

dcl-proc SubmitOrder export;
  dcl-pi *n end-pi;

  exfmt ORENTRY;
  write ORDHDR;
  write ORDLINE;
  AuditEvent('ORDER': OrderId);
  PostBilling(OrderId);
end-proc;

SubmitOrder();
return;
