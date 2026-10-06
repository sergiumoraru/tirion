**free
dcl-f AUDITLOG usage(*output);

dcl-proc CBIAUDLOG export;
  dcl-pi *n;
    Domain char(12) const;
    RefId char(12) const;
  end-pi;

  write AUDITLOG;
end-proc;
