function initLogin() {
  $('#forgot-password').click(function() {
    $(this).parents('li').find('.delete-row').data('id');
    submitLogin();
  });
}

function submitLogin() {}
