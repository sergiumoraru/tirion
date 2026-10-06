**free
ctl-opt dftactgrp(*no) main(INVOICE_SCAN);

/include ../copy/AUDIT_P.RPGLE

dcl-proc INVOICE_SCAN;
  dcl-pi *n;
    Region char(2) const;
  end-pi;
  dcl-s InvoiceId packed(12:0);

  exec sql declare Pending cursor for
    select invoice_id from BILLING/INVOICE_QUEUE
      where region_code = :Region;
  exec sql open Pending;
  if SQLSTT <> '00000';
    RecordFailure('open');
  endif;

  exsr ReadNext;
  dow SQLSTT = '00000';
    exsr ReadNext;
  enddo;
  exec sql close Pending;
  return;

  begsr ReadNext;
    exec sql fetch Pending into :InvoiceId;
    if SQLSTT <> '00000' and SQLSTT <> '02000';
      RecordFailure('fetch');
    endif;
  endsr;
end-proc;
