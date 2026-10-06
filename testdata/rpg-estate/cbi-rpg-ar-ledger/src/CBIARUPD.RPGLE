**free
/copy ../cbi-rpg-shared-services/copy/CBI_SHARED.rpgleinc

dcl-f INVHDR usage(*input);
dcl-f ARLEDGER usage(*update:*output);

dcl-pr AuditLedger extproc('CBIAUDLOG');
  Domain char(12) const;
  RefId char(12) const;
end-pr;

dcl-proc CBIARUPD export;
  dcl-pi *n;
    InvoiceNo char(12) const;
  end-pi;

  chain InvoiceNo INVHDR;
  update ARLEDGER;
  AuditLedger('AR': InvoiceNo);
end-proc;
