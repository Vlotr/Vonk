<?php

declare(strict_types=1);

require __DIR__ . '/../vendor/autoload.php';
require __DIR__ . '/../app/Services/PaymentGateway.php';
require __DIR__ . '/../app/Http/Controllers/CheckoutController.php';

use App\Http\Controllers\CheckoutController;
use Symfony\Component\OptionsResolver\OptionsResolver;

$root = dirname(__DIR__);

\Sentry\init([
    'dsn' => getenv('SENTRY_DSN'),
    'release' => 'webshop@1.4.2',
    'environment' => 'production',
    'server_name' => 'web-1',
    'send_default_pii' => true,
    // What sentry-laravel does: paths relative to the app, vendor code not in-app.
    'prefixes' => [$root],
    'in_app_exclude' => [$root . '/vendor'],
]);

\Sentry\configureScope(function (\Sentry\State\Scope $scope): void {
    $scope->setUser(['id' => '42', 'email' => 'jan@example.com']);
    $scope->setTag('checkout_step', 'payment');
    $scope->setExtra('cart', ['items' => 3, 'payment' => ['method' => 'card', 'card_number' => '4111111111111111']]);
});

\Sentry\addBreadcrumb(new \Sentry\Breadcrumb(
    \Sentry\Breadcrumb::LEVEL_INFO,
    \Sentry\Breadcrumb::TYPE_HTTP,
    'http',
    null,
    ['url' => 'https://payments.example.com/v1/charges?api_key=pk_live_51Hxyz&currency=eur', 'method' => 'POST', 'status_code' => 502]
));

// A tiny "router": vendor code calls the controller, as a framework would.
$resolver = new OptionsResolver();
$resolver->setDefined('response');
$resolver->setNormalizer('response', function ($options, $value) {
    return (new CheckoutController())->store($_POST);
});
echo $resolver->resolve(['response' => null])['response'];
