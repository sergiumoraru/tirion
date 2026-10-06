**free
ctl-opt bnddir('CBISHARED');
/copy ../cbi-rpg-shared-services/copy/CBI_SHARED.rpgleinc

dcl-f ORDHDR usage(*input);
dcl-f INVHDR usage(*output);

dcl-pr UpdateLedger extpgm('CBIARUPD');
  InvoiceNo char(12) const;
end-pr;

dcl-s OrderTotal packed(11:2);
dcl-s TaxAmount packed(11:2);
dcl-s OrderState char(2) inz('CA');
dcl-s InvoiceNo char(12) inz('INV000120001');

dcl-proc CBIBILLP export;
  dcl-pi *n;
    OrderId char(12) const;
  end-pi;

  chain OrderId ORDHDR;

  exec sql
    select order_total
      into :OrderTotal
      from ORDHDR
     where order_id = :OrderId;

  ComputeTax(OrderState: OrderTotal: TaxAmount);
  write INVHDR;
  UpdateLedger(InvoiceNo);
  AuditEvent('BILLING': OrderId);
end-proc;
