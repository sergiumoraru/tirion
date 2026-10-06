**FREE
// excerpted from richardschoen/QshOni QSHONISRV.RPGLE

Ctl-Opt NoMain;

Dcl-PR QcmdExc Extpgm('QCMDEXC');
   *N Char(5000) Const Options(*Varsize);
   *N Packed(15:5) Const;
End-PR;

Dcl-PR QshGetParr Extpgm('QSHGETPARR');
   parm01 Char(255);
   parm02 Char(255);
End-PR;

Dcl-Proc RunClCmd export;
Dcl-PI RunClCmd Int(10:0);
   cmdln VarChar(5000) const;
End-PI;

   QcmdExc(%trim(cmdln): %Len(cmdln));
   Return 0;
End-Proc;

Dcl-Proc QshExec export;
Dcl-PI QshExec Int(10:0);
   cmdln VarChar(5000) const;
End-PI;
Dcl-S qshcmd VarChar(5000) inz('');

   qshcmd = 'QSHONI/QSHEXEC CMDLINE(' + %trim(cmdln) + ')';
   QcmdExc(%trim(qshcmd): %Len(qshcmd));
   Return 0;
End-Proc;

Dcl-Proc QshBash export;
Dcl-PI QshBash Int(10:0);
   cmdln VarChar(5000) const;
End-PI;
Dcl-S bashcmd VarChar(5000) inz('');

   bashcmd = 'QSHONI/QSHBASH CMDLINE(' + %trim(cmdln) + ')';
   QcmdExc(%trim(bashcmd): %Len(bashcmd));
   Return 0;
End-Proc;

Dcl-Proc QshCall export;
Dcl-PI QshCall Int(10:0);
   cmdln VarChar(5000) const;
   rtnparm01 Char(255);
   rtnparm02 Char(255);
End-PI;
Dcl-S callcmd VarChar(5000) inz('');

   callcmd = 'QSHONI/QSHCALL CMDLINE(' + %trim(cmdln) + ')';
   QcmdExc(%trim(callcmd): %Len(callcmd));
   QshGetParr(rtnparm01: rtnparm02);
   Return 0;
End-Proc;
