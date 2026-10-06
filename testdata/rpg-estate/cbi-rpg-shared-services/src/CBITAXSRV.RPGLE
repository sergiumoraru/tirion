**free
dcl-proc CBITAXSRV export;
  dcl-pi *n;
    State char(2) const;
    OrderTotal packed(11:2) const;
    TaxAmount packed(11:2);
  end-pi;

  exec sql
    select rate
      into :TaxAmount
      from TAXRULES
     where state = :State;
end-proc;
